package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// at is a log timestamp, local time, as the gateway writes it.
func at(t time.Time) string { return t.Format("2006/01/02 15:04:05") }

func TestGatewayProblems(t *testing.T) {
	now := time.Date(2026, 11, 24, 13, 0, 0, 0, time.Local)
	ago := func(d time.Duration) string { return at(now.Add(-d)) }
	nak := []string{
		ago(9*time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(9*time.Minute) + ".136323 [ERROR] Received a NAK from the modem: 02 04",
		ago(9*time.Minute) + ".136343 Error creating MMDVM modem: modem returned NAK",
		ago(5*time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(5*time.Minute) + ".1 Error creating MMDVM modem: modem returned NAK",
		ago(1*time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
	}
	healthy := []string{
		ago(time.Hour) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(time.Hour) + ".8 [INFO] Connected to CC1200 modem on /dev/ttyAMA0",
		ago(time.Hour) + ".9 [INFO] Can't reach reflector M17-M17 107.191.121.105:17000 yet (network is unreachable); retrying",
		ago(time.Hour-5*time.Second) + " [INFO] Reached reflector M17-M17 107.191.121.105:17000",
	}
	sx := []string{ // an SX1255 setting with a CC1200 HAT on
		ago(10*time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(10*time.Minute) + ".1 Error creating SX1255 modem: SX1255 init: SX1255 unexpected chip version: 0x00 (expected 0x11)",
	}
	unreachable := []string{
		ago(time.Hour) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(time.Hour) + ".8 [INFO] Connected to MMDVM modem on /dev/ttyAMA0",
		ago(time.Hour) + ".9 [INFO] Can't reach reflector M17-XYZ 1.2.3.4:17000 yet (i/o timeout); retrying",
	}
	for _, tc := range []struct {
		name    string
		log     []string
		running bool
		age     time.Duration
		want    string // the first problem's title, "" for none
		detail  string
	}{
		{"healthy", healthy, true, time.Hour, "", ""},
		{"restart loop", nak, true, 20 * time.Second, "The gateway keeps restarting: it started 3 times", "modem returned NAK"},
		{"no modem", sx, false, 0, "The gateway isn't running.", "unexpected chip version"},
		{"no modem, running", sx, true, 10 * time.Minute, "The gateway can't reach its radio HAT.", "unexpected chip version"},
		{"just started", sx, true, 5 * time.Second, "", ""},
		{"reflector", unreachable, true, time.Hour, "The gateway can't reach the reflector M17-XYZ", ""},
	} {
		ps := gatewayProblems(readGatewayLog(tc.log), tc.running, tc.age, now)
		if tc.want == "" {
			if len(ps) != 0 {
				t.Errorf("%s: problems %+v, want none", tc.name, ps)
			}
			continue
		}
		if len(ps) == 0 || !strings.HasPrefix(ps[0].Title, tc.want) || !strings.Contains(ps[0].Detail, tc.detail) {
			t.Errorf("%s: problems %+v, want %q with %q", tc.name, ps, tc.want, tc.detail)
		}
	}
}

func TestOSAndSystemProblems(t *testing.T) {
	// Installed, not restarted (as m17-repeater was): m17os-upgrade has made
	// the new slot active and pending
	ps := osProblems(OSStatus{Running: "2026.11.25.1", ActiveSlot: "A", PreviousSlot: "B", Pending: true, SlotA: "2026.11.25.2", SlotB: "2026.11.25.1"})
	if len(ps) != 1 || !strings.Contains(ps[0].Title, "2026.11.25.2 is installed: restart") {
		t.Errorf("installed update: %+v", ps)
	}
	// Restarted into it, health check not passed yet
	ps = osProblems(OSStatus{Running: "2026.11.25.2", ActiveSlot: "A", Pending: true, BootTries: 1, SlotA: "2026.11.25.2", SlotB: "2026.11.25.1"})
	if len(ps) != 1 || !strings.Contains(ps[0].Title, "Checking that this new system version starts properly (try 1)") {
		t.Errorf("checking: %+v", ps)
	}
	// Settled, or rolled back (the other slot newer, but not active): nothing
	if ps := osProblems(OSStatus{Running: "2026.11.25.1", ActiveSlot: "B", SlotA: "2026.11.25.2", SlotB: "2026.11.25.1"}); len(ps) != 0 {
		t.Errorf("after a rollback: %+v", ps)
	}
	ps = systemProblems(SystemInfo{DiskTotal: 24 << 30, DiskFree: 100 << 20, HasTemp: true, TempC: 83})
	if len(ps) != 2 {
		t.Errorf("full and hot: %+v", ps)
	}
}

func TestProcessAge(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uptime"), []byte("1000.00 0\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "4242"), 0o755)
	os.WriteFile(filepath.Join(dir, "4242", "comm"), []byte("m17-gateway\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "4242", "stat"), []byte("4242 (m17-gateway) S 1 4242 4242 0 -1 0 0 0 0 0 0 0 0 0 20 0 9 0 40000 0 0\n"), 0o644)
	age, ok := processAge(dir, "m17-gateway")
	if !ok || age != 600*time.Second {
		t.Errorf("age %v %v, want 10m", age, ok)
	}
	if _, ok := processAge(dir, "nothing"); ok {
		t.Error("found a process that isn't there")
	}
}
