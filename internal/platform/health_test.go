package platform

import (
	"errors"
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
		ago(9*time.Minute+30*time.Second) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(9*time.Minute+30*time.Second) + ".1 Error creating MMDVM modem: modem returned NAK",
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
	// Restarts asked for: each run had connected
	restarted := []string{
		ago(3*time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(3*time.Minute) + ".9 [INFO] Connected to CC1200 modem on /dev/ttyAMA0",
		ago(3*time.Minute-20*time.Second) + ".2 [ERROR] Error reading cmd from modem: Port has been closed",
		ago(3*time.Minute-20*time.Second) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(3*time.Minute-21*time.Second) + ".1 [INFO] Connected to CC1200 modem on /dev/ttyAMA0",
		ago(2*time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(2*time.Minute) + ".5 [INFO] Connected to CC1200 modem on /dev/ttyAMA0",
		ago(time.Minute) + " [INFO] Loading settings from '/etc/m17-gateway.ini'",
		ago(time.Minute) + ".5 [INFO] Connected to CC1200 modem on /dev/ttyAMA0",
	}
	// A crash after connecting, each time
	var panics []string
	for i := 4; i > 0; i-- {
		panics = append(panics,
			ago(time.Duration(i)*time.Minute)+" [INFO] Loading settings from '/etc/m17-gateway.ini'",
			ago(time.Duration(i)*time.Minute)+".5 [INFO] Connected to CC1200 modem on /dev/ttyAMA0",
			"panic: runtime error: index out of range [3] with length 2")
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
		{"restart loop", nak, true, 20 * time.Second, "The gateway keeps failing: it stopped 3 times", "modem returned NAK"},
		{"restarts asked for", restarted, true, time.Minute, "", ""},
		{"panic loop", panics, true, 30 * time.Second, "The gateway keeps failing: it stopped 3 times", ""},
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

// TestAlpineGateway: the gateway's state from rc-service's status output
// (which exits non-zero when stopped) and /proc; restart falling back to
// stop and start on a helper without it.
func TestAlpineGateway(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	os.MkdirAll(filepath.Join(proc, "4242"), 0o755)
	os.WriteFile(filepath.Join(proc, "uptime"), []byte("1000.00 0\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "4242", "comm"), []byte("m17-gateway\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "4242", "stat"), []byte("4242 (m17-gateway) S 1 4242 4242 0 -1 0 0 0 0 0 0 0 0 0 20 0 9 0 40000 0 0\n"), 0o644)
	os.Symlink("/etc/init.d/m17-gateway", filepath.Join(dir, "runlevel"))
	calls := filepath.Join(dir, "calls")
	helper := func(status, restart string) {
		os.WriteFile(filepath.Join(dir, "helper"), []byte(`#!/bin/sh
echo "$*" >> `+calls+`
case "$2" in
	status) echo " * status: `+status+`"; [ "`+status+`" = started ] ;;
	restart) `+restart+` ;;
	*) exit 0 ;;
esac
`), 0o755)
	}
	a := NewAlpine()
	a.Helper = []string{filepath.Join(dir, "helper")}
	a.Files.Uptime = filepath.Join(proc, "uptime")
	a.HealthPaths.Runlevel = filepath.Join(dir, "runlevel")

	helper("started", "exit 0")
	st, err := a.Gateway(t.Context())
	if err != nil || st != (GatewayState{State: "started", Running: true, Uptime: 600 * time.Second, AtBoot: true}) {
		t.Errorf("running: %+v %v", st, err)
	}
	os.RemoveAll(filepath.Join(proc, "4242"))
	os.Remove(filepath.Join(dir, "runlevel"))
	helper("stopped", "exit 0")
	if st, err := a.Gateway(t.Context()); err != nil || st != (GatewayState{State: "stopped"}) {
		t.Errorf("stopped: %+v %v", st, err)
	}

	os.Remove(calls)
	if err := a.ControlGateway(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	helper("stopped", `echo "admin-helper: usage: gateway stop|start|status" >&2; exit 2`)
	if err := a.ControlGateway(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	if got, want := string(b), "gateway restart\ngateway restart\ngateway stop\ngateway start\n"; got != want {
		t.Errorf("helper calls %q, want %q", got, want)
	}
	if err := a.ControlGateway(t.Context(), "reload"); err == nil {
		t.Error("reload accepted")
	}
}

// TestAlpineGatewayConfig: reading the file (and whether a previous one
// is kept beside the real file, behind the link), and the helper's check
// and save results.
func TestAlpineGatewayConfig(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	os.WriteFile(filepath.Join(dir, "data", "m17-gateway.ini"), []byte("[General]\nCallsign=N1ADJ\n"), 0o644)
	os.Symlink(filepath.Join(dir, "data", "m17-gateway.ini"), filepath.Join(dir, "m17-gateway.ini"))
	os.WriteFile(filepath.Join(dir, "helper"), []byte(`#!/bin/sh
in=$(cat)
case "$1:$in" in
	*:*OLD*) echo "admin-helper: this m17-gateway can't check settings; update it first" >&2; exit 2 ;;
	*:*BAD*) echo "error: bad callsign"; echo "warning: [General] Typo is ignored"; exit 1 ;;
	gateway-config-check:*) echo "warning: [General] Typo is ignored" ;;
	gateway-config-save:*) echo saved ;;
	*) echo "admin-helper: unknown command: $1" >&2; exit 2 ;;
esac
`), 0o755)
	a := NewAlpine()
	a.Helper = []string{filepath.Join(dir, "helper")}
	a.GatewayINI = filepath.Join(dir, "m17-gateway.ini")
	ctx := t.Context()

	gc, err := a.GatewayConfig(ctx)
	if err != nil || gc.Text != "[General]\nCallsign=N1ADJ\n" || gc.HasPrevious {
		t.Errorf("config: %+v %v", gc, err)
	}
	os.WriteFile(filepath.Join(dir, "data", "m17-gateway.ini.prev"), nil, 0o644)
	if gc, _ := a.GatewayConfig(ctx); !gc.HasPrevious {
		t.Error("previous file beside the real one not found")
	}

	c, err := a.CheckGatewayConfig(ctx, "Callsign=W1AW\n")
	if err != nil || !c.OK() || len(c.Warnings) != 1 || c.Warnings[0] != "[General] Typo is ignored" {
		t.Errorf("check, ok: %+v %v", c, err)
	}
	c, err = a.SaveGatewayConfig(ctx, "Callsign=BAD\n")
	if err != nil || c.OK() || c.Errors[0] != "bad callsign" || len(c.Warnings) != 1 {
		t.Errorf("save, errors: %+v %v", c, err)
	}
	if c, err := a.SaveGatewayConfig(ctx, "Callsign=W1AW\n"); err != nil || !c.OK() {
		t.Errorf("save, ok: %+v %v", c, err)
	}
	if _, err := a.SaveGatewayConfig(ctx, "Callsign=OLD\n"); !errors.Is(err, ErrNoConfigCheck) {
		t.Errorf("old gateway: %v", err)
	}
	if err := a.RestoreGatewayConfig(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("old helper: %v", err)
	}
	if _, err := a.CheckGatewayConfig(ctx, strings.Repeat("x", MaxGatewayConfig+1)); err == nil {
		t.Error("an oversized file accepted")
	}
}
