package activity

import (
	"bufio"
	"os"
	"path/filepath"
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
	if err := New(filepath.Join(t.TempDir(), "none.json"), 0).Load(); err != nil {
		t.Errorf("missing file: %v", err)
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
