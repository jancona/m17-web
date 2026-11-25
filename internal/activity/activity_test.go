package activity

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// replay feeds a file of messages, one per line, to the log.
func replay(t *testing.T, l *Log, file string) {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if err := l.Handle(sc.Bytes()); err != nil {
			t.Fatalf("%s: %v", sc.Text(), err)
		}
	}
}

// TestGatewaySession replays m17-gateway's sample session (a copy of its
// cmd/m17-gateway/testdata/mqtt/session.jsonl).
func TestGatewaySession(t *testing.T) {
	l := New("", 0)
	replay(t, l, "testdata/session.jsonl")
	st, entries := l.Snapshot()

	if st.Gateway != "m17-gateway is stopping" || st.ModemOpen == nil || *st.ModemOpen || st.Mode != "M17" {
		t.Errorf("status = %+v", st)
	}
	if st.ModemState() != "closed" || (Status{}).ModemState() != "unknown" {
		t.Errorf("ModemState = %q", st.ModemState())
	}
	if st.Link == nil || st.Link.Action != "unlinked" || st.Link.Describe() != "unlinked from M17-M17 C" {
		t.Errorf("link = %+v", st.Link)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(entries), entries)
	}
	// Newest first: data packet, SMS, network voice, RF voice
	data, sms, net, rf := entries[0], entries[1], entries[2], entries[3]
	if data.Kind != "packet" || data.Data != "81DEAD" || *data.Protocol != 129 || data.Text != nil || data.BER != nil {
		t.Errorf("data packet = %+v", data)
	}
	if sms.Kind != "packet" || sms.Text == nil || *sms.Text != "hi" || sms.Dst != "PARROT" || sms.Source != "rf" || sms.BER == nil || *sms.BER != 7.25 {
		t.Errorf("SMS = %+v", sms)
	}
	if net.Src != "AA5RL" || !net.LateEntry || !net.Lost || net.Active || *net.Duration != 1.6 || net.BER != nil {
		t.Errorf("network voice = %+v", net)
	}
	if rf.Src != "N1ADJ" || rf.Dst != "ALL" || rf.Lost || *rf.Duration != 6 || *rf.BER != 1.5 || *rf.CAN != 2 {
		t.Errorf("RF voice = %+v", rf)
	}
	if rf.Position == nil || rf.Position.Latitude != 43.878414 || *rf.Position.Bearing != 35 {
		t.Errorf("RF voice position = %+v", rf.Position)
	}
}

func TestVoiceInProgress(t *testing.T) {
	l := New("", 0)
	msg := func(s string) {
		if err := l.Handle([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	msg(`{"M17":{"timestamp":"2026-11-20T01:00:00.000Z","src_callsign":"N1ADJ","dst_callsign":"ALL","source":"rf","action":"start"}}`)
	_, e := l.Snapshot()
	if len(e) != 1 || !e[0].Active {
		t.Fatalf("after start: %+v", e)
	}
	// A new start without an end: the first one was lost
	msg(`{"M17":{"timestamp":"2026-11-20T01:00:05.000Z","src_callsign":"W1AW","dst_callsign":"ALL","source":"rf","action":"start"}}`)
	// An end on the other source belongs to nothing
	msg(`{"M17":{"timestamp":"2026-11-20T01:00:06.000Z","source":"network","action":"end","duration":1}}`)
	_, e = l.Snapshot()
	if len(e) != 2 || !e[0].Active || e[1].Active || !e[1].Lost {
		t.Fatalf("after second start: %+v", e)
	}
	// A position long after the transmission ended isn't attached
	msg(`{"M17GNSS":{"timestamp":"2026-11-20T01:05:00.000Z","source":"rf","src_callsign":"N1ADJ","latitude":1,"longitude":2}}`)
	// One for the transmission in progress is
	msg(`{"M17GNSS":{"timestamp":"2026-11-20T01:00:07.000Z","source":"rf","src_callsign":"W1AW","latitude":3,"longitude":4}}`)
	_, e = l.Snapshot()
	if e[1].Position != nil || e[0].Position == nil || e[0].Position.Latitude != 3 {
		t.Errorf("positions: %+v / %+v", e[0].Position, e[1].Position)
	}
}

func TestMaxAndSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lastheard.json")
	l := New(path, 3)
	for i := range 5 {
		ts := time.Date(2026, 11, 20, 1, 0, i, 0, time.UTC).Format(time.RFC3339)
		if err := l.Handle([]byte(`{"M17Packet":{"timestamp":"` + ts + `","source":"rf","src_callsign":"N1ADJ","dst_callsign":"ALL","protocol":5,"text":"x"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	// One left in progress: saved, but no longer active once loaded
	if err := l.Handle([]byte(`{"M17":{"timestamp":"2026-11-20T01:00:09.000Z","src_callsign":"W1AW","dst_callsign":"ALL","source":"rf","action":"start"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi == nil {
		t.Fatal("not saved")
	}
	// Nothing changed: not written again
	os.Remove(path)
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("saved again without a change")
	}
	l.dirty = true
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}

	l2 := New(path, 3)
	if err := l2.Load(); err != nil {
		t.Fatal(err)
	}
	_, e := l2.Snapshot()
	if len(e) != 3 || e[0].Src != "W1AW" || e[0].Active || e[2].Time.Second() != 3 {
		t.Errorf("loaded: %+v", e)
	}
	// Text messages are kept apart, so all five survive
	if m := l2.Messages(); len(m) != 5 || m[0].Time.Second() != 4 || m[4].Time.Second() != 0 {
		t.Errorf("loaded messages: %+v", m)
	}
	if err := New(filepath.Join(t.TempDir(), "none.json"), 0).Load(); err != nil {
		t.Errorf("missing file: %v", err)
	}
}

func TestMessages(t *testing.T) {
	l := New("", 2)
	msg := func(s string) {
		t.Helper()
		if err := l.Handle([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	msg(`{"M17Packet":{"timestamp":"2026-11-20T01:00:00Z","source":"rf","src_callsign":"N1ADJ","dst_callsign":"@ALL","protocol":5,"text":"hello"}}`)
	msg(`{"M17Packet":{"timestamp":"2026-11-20T01:00:01Z","source":"rf","src_callsign":"N1ADJ","dst_callsign":"ALL","protocol":129,"data":"81DEAD"}}`)
	for i := range MaxMessages + 5 {
		ts := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		msg(`{"M17":{"timestamp":"` + ts + `","src_callsign":"W1AW","dst_callsign":"ALL","source":"network","action":"start"}}`)
	}
	m := l.Messages()
	if len(m) != 1 || *m[0].Text != "hello" || m[0].Dst != "@ALL" {
		t.Fatalf("messages after voice traffic: %+v", m)
	}
	for i := range MaxMessages + 5 {
		msg(`{"M17Packet":{"timestamp":"2026-11-20T03:00:00Z","source":"network","src_callsign":"AA5RL","dst_callsign":"N1ADJ","protocol":5,"text":"` + string(rune('a'+i%26)) + `"}}`)
	}
	if m := l.Messages(); len(m) != MaxMessages || m[0].Src != "AA5RL" {
		t.Errorf("got %d messages, want %d", len(m), MaxMessages)
	}

	// A log saved before messages were kept apart
	path := filepath.Join(t.TempDir(), "lastheard.json")
	os.WriteFile(path, []byte(`{"entries":[{"time":"2026-11-20T01:00:00Z","kind":"packet","src":"N1ADJ","dst":"ALL","text":"old"},{"time":"2026-11-20T01:00:01Z","kind":"voice","src":"N1ADJ","dst":"ALL"}]}`), 0o600)
	old := New(path, 0)
	if err := old.Load(); err != nil {
		t.Fatal(err)
	}
	if m := old.Messages(); len(m) != 1 || *m[0].Text != "old" {
		t.Errorf("messages from an old log: %+v", m)
	}
}

func TestBadMessages(t *testing.T) {
	l := New("", 0)
	if err := l.Handle([]byte(`not json`)); err == nil {
		t.Error("no error for bad JSON")
	}
	if err := l.Handle([]byte(`{"M17":"x"}`)); err == nil {
		t.Error("no error for a bad M17 object")
	}
	if err := l.Handle([]byte(`{"DMR":{"action":"start"}}`)); err != nil {
		t.Errorf("other modes should be ignored: %v", err)
	}
}

func TestGatewayRunning(t *testing.T) {
	status := `{"status":{"timestamp":"2026-11-20T01:02:03.004Z","message":"m17-gateway is starting"}}`
	modem := `{"Modem":{"timestamp":"2026-11-20T01:02:03.004Z","open":true,"description":"x"}}`
	// Either order: retained messages on different topics arrive in any
	for _, order := range [][]string{{status, modem}, {modem, status}} {
		l := New("", 0)
		for _, m := range order {
			if err := l.Handle([]byte(m)); err != nil {
				t.Fatal(err)
			}
		}
		if st, _ := l.Snapshot(); st.Gateway != "running" {
			t.Errorf("Gateway = %q, want running", st.Gateway)
		}
	}
}

// TestChanged: a message wakes every watcher; nothing else does.
func TestChanged(t *testing.T) {
	l := New("", 0)
	a, b := l.Changed(), l.Changed()
	select {
	case <-a:
		t.Fatal("woken without a change")
	default:
	}
	if err := l.Handle([]byte(`{"MMDVM":{"timestamp":"2026-11-25T12:00:00Z","mode":"idle"}}`)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []<-chan struct{}{a, b} {
		select {
		case <-c:
		default:
			t.Error("a watcher wasn't woken")
		}
	}
	c := l.Changed()
	l.SetBroker(true)
	select {
	case <-c:
	default:
		t.Error("the broker coming up didn't wake the watcher")
	}
}

func TestPositions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lastheard.json")
	l := New(path, 0)
	replay(t, l, "testdata/session.jsonl")
	p := l.Positions(time.Time{})
	if len(p) != 1 || p[0].Callsign != "N1ADJ" || p[0].Latitude != 43.878414 || p[0].Source != "rf" {
		t.Fatalf("positions from the session: %+v", p)
	}
	msg := func(s string) {
		t.Helper()
		if err := l.Handle([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	// A position with no transmission heard here still counts
	msg(`{"M17GNSS":{"timestamp":"2026-11-21T01:00:00Z","source":"network","src_callsign":"W1AW    ","latitude":41.7,"longitude":-72.7,"station_type":0}}`)
	// An older one for the same station doesn't replace it
	msg(`{"M17GNSS":{"timestamp":"2026-11-21T00:00:00Z","source":"network","src_callsign":"w1aw","latitude":1,"longitude":1,"station_type":0}}`)
	p = l.Positions(time.Time{})
	if len(p) != 2 || p[0].Callsign != "W1AW" || p[0].Latitude != 41.7 {
		t.Fatalf("positions: %+v", p)
	}
	if p := l.Positions(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)); len(p) != 1 || p[0].Callsign != "W1AW" {
		t.Errorf("positions since: %+v", p)
	}
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	l2 := New(path, 0)
	if err := l2.Load(); err != nil {
		t.Fatal(err)
	}
	if p := l2.Positions(time.Time{}); len(p) != 2 {
		t.Errorf("loaded positions: %+v", p)
	}

	// No more than MaxStations
	for i := range MaxStations + 3 {
		ts := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		msg(`{"M17GNSS":{"timestamp":"` + ts + `","source":"rf","src_callsign":"T` + strconv.Itoa(i) + `","latitude":1,"longitude":1,"station_type":0}}`)
	}
	if p := l.Positions(time.Time{}); len(p) != MaxStations || p[len(p)-1].Callsign != "T3" {
		t.Errorf("got %d positions, oldest %+v", len(p), p[len(p)-1])
	}

	// A log saved before positions were kept apart
	old := filepath.Join(t.TempDir(), "old.json")
	os.WriteFile(old, []byte(`{"entries":[{"time":"2026-11-20T01:00:00Z","kind":"voice","source":"rf","src":"N1ADJ","dst":"ALL","position":{"latitude":43.8,"longitude":-70,"station_type":0}}]}`), 0o600)
	l3 := New(old, 0)
	if err := l3.Load(); err != nil {
		t.Fatal(err)
	}
	if p := l3.Positions(time.Time{}); len(p) != 1 || p[0].Callsign != "N1ADJ" || p[0].Longitude != -70 {
		t.Errorf("positions from an old log: %+v", p)
	}
}
