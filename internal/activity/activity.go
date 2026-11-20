// Package activity keeps the hotspot's recent activity (last heard, text
// messages, positions) and status (reflector link, mode, modem) from the
// messages m17-gateway publishes over MQTT on <Name>/json. MQTT keeps no
// history, so this is where it lives: a bounded list saved to the state
// directory now and then, rarely enough to spare the SD card.
//
// The messages follow M17-Project/MMDVMHost's schema.json, so an
// MMDVMHost-based M17 hotspot can feed this too. See m17-gateway's
// docs/mqtt-design.md.
package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultMax is how many entries the log keeps.
const DefaultMax = 200

// gnssWindow is how long after a transmission ends a position from the same
// station still belongs to it (the gateway sends one every 15 s at most).
const gnssWindow = 30 * time.Second

// Entry is a voice transmission or a packet.
type Entry struct {
	Time        time.Time `json:"time"` // when it started
	Kind        string    `json:"kind"` // voice or packet
	Source      string    `json:"source"`
	Src         string    `json:"src"`
	Dst         string    `json:"dst"`
	CAN         *int      `json:"can,omitempty"`
	TrafficType string    `json:"traffic_type,omitempty"`
	// Voice
	Active    bool     `json:"active,omitempty"`     // still being heard
	LateEntry bool     `json:"late_entry,omitempty"` // heard from the middle
	Lost      bool     `json:"lost,omitempty"`       // ended without an end frame
	Duration  *float64 `json:"duration,omitempty"`   // seconds
	BER       *float64 `json:"ber,omitempty"`        // percent, RF only
	// Packet
	Protocol *int    `json:"protocol,omitempty"`
	Text     *string `json:"text,omitempty"` // SMS
	Data     string  `json:"data,omitempty"` // other packets, hex
	// Position the station sent during the transmission
	Position *Position `json:"position,omitempty"`

	end time.Time
}

// Position is from an M17GNSS message.
type Position struct {
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	Altitude    *float64 `json:"altitude,omitempty"`
	Speed       *float64 `json:"speed,omitempty"`
	Bearing     *int     `json:"bearing,omitempty"`
	StationType int      `json:"station_type"`
}

// Link is the reflector link, from the gateway's retained link message.
type Link struct {
	Time      time.Time `json:"time"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason,omitempty"`
	Reflector string    `json:"reflector,omitempty"`
	Module    string    `json:"module,omitempty"`
}

// Linked reports whether the link is up.
func (l *Link) Linked() bool {
	return l != nil && l.Action == "linking" && l.Reason == "established"
}

// Status is what the gateway last said about itself.
type Status struct {
	Broker    bool      `json:"broker"`            // connected to the broker
	Heard     time.Time `json:"heard,omitzero"`    // last message from the gateway
	Gateway   string    `json:"gateway,omitempty"` // last lifecycle message
	Mode      string    `json:"mode,omitempty"`    // idle, M17, ...
	ModemOpen *bool     `json:"modem_open,omitempty"` // nil: not heard since we subscribed
	Modem     string    `json:"modem,omitempty"` // description
	Link      *Link     `json:"link,omitempty"`
}

// Log is the activity log. Its methods are safe to call from any goroutine.
type Log struct {
	path string // where it's saved; "" for nowhere
	max  int
	now  func() time.Time

	mu      sync.Mutex
	entries []*Entry          // oldest first
	current map[string]*Entry // voice in progress, by source
	status  Status
	dirty   bool
}

// New returns an empty log saved to path ("" keeps it in memory only).
func New(path string, max int) *Log {
	if max <= 0 {
		max = DefaultMax
	}
	return &Log{path: path, max: max, now: time.Now, current: map[string]*Entry{}}
}

// Load reads the saved log. A missing file is not an error.
func (l *Log) Load() error {
	if l.path == "" {
		return nil
	}
	b, err := os.ReadFile(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved struct {
		Entries []*Entry `json:"entries"`
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		return fmt.Errorf("%s: %w", l.path, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range saved.Entries {
		e.Active = false // whatever was in progress ended while we were away
	}
	if len(saved.Entries) > l.max {
		saved.Entries = saved.Entries[len(saved.Entries)-l.max:]
	}
	l.entries = saved.Entries
	return nil
}

// Save writes the log if it changed since the last save.
func (l *Log) Save() error {
	if l.path == "" {
		return nil
	}
	l.mu.Lock()
	if !l.dirty {
		l.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(struct {
		Entries []*Entry `json:"entries"`
	}{l.entries})
	l.dirty = false
	l.mu.Unlock()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".lastheard-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err2 := tmp.Close(); err == nil {
		err = err2
	}
	if err == nil {
		err = os.Rename(tmp.Name(), l.path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		l.mu.Lock()
		l.dirty = true
		l.mu.Unlock()
	}
	return err
}

// SetBroker records whether the broker connection is up.
func (l *Log) SetBroker(up bool) {
	l.mu.Lock()
	l.status.Broker = up
	l.mu.Unlock()
}

// Snapshot returns the status and the entries, newest first.
func (l *Log) Snapshot() (Status, []Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.status
	if st.Link != nil {
		link := *st.Link
		st.Link = &link
	}
	if st.ModemOpen != nil {
		open := *st.ModemOpen
		st.ModemOpen = &open
	}
	out := make([]Entry, 0, len(l.entries))
	for i := len(l.entries) - 1; i >= 0; i-- {
		out = append(out, *l.entries[i])
	}
	return st, out
}

// The messages, as m17-gateway (and MMDVMHost) publish them. Only the fields
// used here.
type (
	mmdvmMsg struct {
		Mode    string `json:"mode"`
		Message string `json:"message"`
	}
	modemMsg struct {
		Open        bool   `json:"open"`
		Description string `json:"description"`
	}
	linkMsg struct {
		Timestamp string `json:"timestamp"`
		Action    string `json:"action"`
		Reason    string `json:"reason"`
		Reflector string `json:"reflector"`
		Module    string `json:"module"`
	}
	m17Msg struct {
		Timestamp   string   `json:"timestamp"`
		Src         string   `json:"src_callsign"`
		Dst         string   `json:"dst_callsign"`
		Source      string   `json:"source"`
		Action      string   `json:"action"`
		TrafficType string   `json:"traffic_type"`
		CAN         *int     `json:"can"`
		Duration    *float64 `json:"duration"`
		BER         *float64 `json:"ber"`
	}
	packetMsg struct {
		Timestamp string  `json:"timestamp"`
		Source    string  `json:"source"`
		Src       string  `json:"src_callsign"`
		Dst       string  `json:"dst_callsign"`
		Protocol  int     `json:"protocol"`
		Text      *string `json:"text"`
		Data      string  `json:"data"`
		CAN       *int    `json:"can"`
	}
	gnssMsg struct {
		Timestamp string `json:"timestamp"`
		Source    string `json:"source"`
		Src       string `json:"src_callsign"`
		Position
	}
)

// Handle takes one message from <Name>/json: an object with one key naming
// its type. Types it doesn't use are ignored.
func (l *Log) Handle(payload []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status.Heard = l.now()
	for typ, raw := range m {
		var err error
		switch typ {
		case "MMDVM":
			var v mmdvmMsg
			if err = json.Unmarshal(raw, &v); err == nil {
				if v.Mode != "" {
					l.status.Mode = v.Mode
				}
				if v.Message != "" {
					l.status.Gateway = v.Message
				}
			}
		case "status":
			var v mmdvmMsg
			if err = json.Unmarshal(raw, &v); err == nil && v.Message != "" {
				l.status.Gateway = v.Message
			}
		case "Modem":
			var v modemMsg
			if err = json.Unmarshal(raw, &v); err == nil {
				open := v.Open
				l.status.ModemOpen = &open
				if v.Description != "" || !v.Open {
					l.status.Modem = v.Description
				}
			}
		case "link":
			var v linkMsg
			if err = json.Unmarshal(raw, &v); err == nil {
				l.status.Link = &Link{Time: l.parseTime(v.Timestamp), Action: v.Action, Reason: v.Reason,
					Reflector: v.Reflector, Module: v.Module}
			}
		case "M17":
			var v m17Msg
			if err = json.Unmarshal(raw, &v); err == nil {
				l.voice(v)
			}
		case "M17Packet":
			var v packetMsg
			if err = json.Unmarshal(raw, &v); err == nil {
				p := v.Protocol
				l.add(&Entry{Time: l.parseTime(v.Timestamp), Kind: "packet", Source: v.Source,
					Src: v.Src, Dst: v.Dst, CAN: v.CAN, Protocol: &p, Text: v.Text, Data: v.Data})
			}
		case "M17GNSS":
			var v gnssMsg
			if err = json.Unmarshal(raw, &v); err == nil {
				l.gnss(v)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", typ, err)
		}
	}
	return nil
}

func (l *Log) voice(v m17Msg) {
	t := l.parseTime(v.Timestamp)
	switch v.Action {
	case "start", "late_entry":
		if cur := l.current[v.Source]; cur != nil {
			cur.Active, cur.Lost, cur.end = false, true, t // never heard its end
		}
		e := &Entry{Time: t, Kind: "voice", Source: v.Source, Src: v.Src, Dst: v.Dst, CAN: v.CAN,
			TrafficType: v.TrafficType, Active: true, LateEntry: v.Action == "late_entry"}
		l.current[v.Source] = e
		l.add(e)
	case "end", "lost":
		e := l.current[v.Source]
		if e == nil {
			return // started before we were listening
		}
		delete(l.current, v.Source)
		e.Active, e.Lost, e.Duration, e.BER, e.end = false, v.Action == "lost", v.Duration, v.BER, t
		l.dirty = true
	}
}

// gnss attaches a position to the transmission it came with: the one in
// progress from that station, or its most recent one if that just ended.
func (l *Log) gnss(v gnssMsg) {
	t := l.parseTime(v.Timestamp)
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if e.Kind != "voice" || e.Source != v.Source || !sameStation(e.Src, v.Src) {
			continue
		}
		if e.Active || t.Sub(e.end) <= gnssWindow {
			pos := v.Position
			e.Position = &pos
			l.dirty = true
		}
		return
	}
}

// sameStation compares callsigns, ignoring padding.
func sameStation(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (l *Log) add(e *Entry) {
	l.entries = append(l.entries, e)
	if n := len(l.entries) - l.max; n > 0 {
		l.entries = slices.Delete(l.entries, 0, n)
	}
	l.dirty = true
}

func (l *Log) parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return l.now().UTC()
}
