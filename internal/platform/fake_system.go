package platform

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// fakeSystem is the Fake's state for the admin pages.
type fakeSystem struct {
	saved   []SavedNetwork
	keys    []SSHKey
	os      OSStatus
	jobs    map[string]JobStatus
	started time.Time
	// Reboots and Shutdowns count the calls, for tests.
	Reboots, Shutdowns int
	// Unsupported makes every System method fail as on an old image.
	Unsupported bool
}

func newFakeSystem() fakeSystem {
	return fakeSystem{
		saved: []SavedNetwork{{SSID: "HomeNetwork", Priority: 2}, {SSID: "Shack", Priority: 1, Open: true}},
		// No keys, like Current.SSHKeys: the wizard's SSH step adds them
		os: OSStatus{
			Running: "2026.11.23.1", ActiveSlot: "A", PreviousSlot: "B",
			SlotA: "2026.11.23.1", SlotB: "2026.11.20.1", PreviousVersion: "2026.11.20.1",
			LastUpdate: map[string]string{"TIME": "2026-11-24 03:02:40 EST", "RESULT": "installed", "OS_INSTALLED": "2026.11.23.1"},
		},
		jobs:    map[string]JobStatus{},
		started: time.Now(),
	}
}

func (f *Fake) unsupported() error {
	if f.sys.Unsupported {
		return ErrUnsupported
	}
	return nil
}

func (f *Fake) SystemInfo(ctx context.Context) (SystemInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return SystemInfo{
		Board: "Raspberry Pi Zero 2 W Rev 1.0", Hostname: f.current.Hostname,
		OSVersion: f.sys.os.Running, Kernel: "6.18.60+rpt-rpi-v8",
		Uptime: 6*time.Hour + 49*time.Minute + time.Since(f.sys.started).Truncate(time.Second),
		TempC:  48.3, HasTemp: true,
		MemTotal: 463 << 20, MemAvailable: 381 << 20,
		DiskTotal: 24 << 30, DiskFree: 23 << 30,
		Load:      "0.12 0.20 0.18",
		Addresses: []string{"wlan0 192.168.1.94"},
		Modem:     f.hat.Type, ModemIdent: f.hat.Ident, ModemAt: f.sys.started,
	}, nil
}

func (f *Fake) SavedWiFi(ctx context.Context) ([]SavedNetwork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return nil, err
	}
	out := slices.Clone(f.sys.saved)
	slices.SortStableFunc(out, func(a, b SavedNetwork) int { return b.Priority - a.Priority })
	return out, nil
}

func (f *Fake) WiFiStatus(ctx context.Context) (WiFiStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return WiFiStatus{}, err
	}
	if len(f.sys.saved) == 0 {
		return WiFiStatus{State: "DISCONNECTED"}, nil
	}
	return WiFiStatus{State: "COMPLETED", SSID: f.sys.saved[0].SSID, Signal: -52, FreqMHz: 2437, Address: "192.168.1.94"}, nil
}

func (f *Fake) AddWiFi(ctx context.Context, ssid, password string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	top := 0
	for _, n := range f.sys.saved {
		top = max(top, n.Priority)
	}
	f.sys.saved = slices.DeleteFunc(f.sys.saved, func(n SavedNetwork) bool { return n.SSID == ssid })
	f.sys.saved = append(f.sys.saved, SavedNetwork{SSID: ssid, Priority: top + 1, Open: password == ""})
	return nil
}

func (f *Fake) RemoveWiFi(ctx context.Context, ssid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	n := len(f.sys.saved)
	f.sys.saved = slices.DeleteFunc(f.sys.saved, func(n SavedNetwork) bool { return n.SSID == ssid })
	if len(f.sys.saved) == n {
		return fmt.Errorf("admin helper wifi-remove: no saved network named %s", ssid)
	}
	return nil
}

func (f *Fake) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return nil, err
	}
	return slices.Clone(f.sys.keys), nil
}

func (f *Fake) RemoveSSHKey(ctx context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	n := len(f.sys.keys)
	f.sys.keys = slices.DeleteFunc(f.sys.keys, func(k SSHKey) bool { return k.Key == key })
	if len(f.sys.keys) == n {
		return errors.New("admin helper ssh-key-remove: no such key")
	}
	f.current.SSHKeys = len(f.sys.keys)
	return nil
}

func (f *Fake) OSStatus(ctx context.Context) (OSStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return OSStatus{}, err
	}
	return f.sys.os, nil
}

// StartJob finishes at once: a check finds 2026.11.25.1; an install puts
// it in the inactive slot.
func (f *Fake) StartJob(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	now := time.Now().Unix()
	switch name {
	case "os-check":
		f.sys.jobs[name] = JobStatus{State: "ok", Exit: 0, Started: now, Log: []string{"m17-hotspot-os update check", "Current:    2026.11.23.1", "Available:  2026.11.25.1 (2026-11-25)", "Signed:     true", "Status:     update available"}}
	case "os-install":
		f.sys.jobs[name] = JobStatus{State: "ok", Exit: 0, Started: now, Log: []string{"Downloading m17-hotspot-os-2026.11.25.1.rootfs.tar.xz...", "Update 2026.11.25.1 installed to slot B.", "Reboot to activate it."}}
		f.sys.os.SlotB = "2026.11.25.1"
	default:
		return fmt.Errorf("admin helper job-start: unknown job: %s", name)
	}
	return nil
}

func (f *Fake) JobStatus(ctx context.Context, name string) (JobStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sys.jobs[name], nil
}

func (f *Fake) Rollback(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	f.sys.Reboots++
	return nil
}

func (f *Fake) Log(ctx context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case "events":
		return []string{
			"2026-11-24 03:02:40 upgrade: installed 2026.11.23.1 to slot A (from slot B, 2026.11.20.1)",
			"2026-11-24 03:02:42 boot-watchdog: Slot A pending: boot try 1 of 3",
			"2026-11-24 03:02:55 health-check: Slot A passed after 10s - marked as good",
		}, nil
	case "gateway":
		return []string{
			"2026/11/24 03:02:43 [INFO] Loading settings from '/etc/m17-gateway.ini'",
			"2026/11/24 03:02:45.090770 [INFO] Connected to MMDVM modem on /dev/ttyAMA0",
			"2026/11/24 03:02:50.104330 [INFO] Reached reflector M17-M17 107.191.121.105:17000",
		}, nil
	case "system":
		if f.sys.Unsupported {
			return nil, ErrUnsupported
		}
		return []string{"Nov 24 03:02:42 m17-hotspot daemon.info m17os: boot-watchdog: Slot A pending"}, nil
	case "web":
		return []string{"time=2026-11-24T03:02:43Z level=INFO msg=listening addr=http://127.0.0.1:8090/setup/"}, nil
	}
	return nil, errors.New("no such log")
}

func (f *Fake) Reboot(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	f.sys.Reboots++
	return nil
}

func (f *Fake) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.unsupported(); err != nil {
		return err
	}
	f.sys.Shutdowns++
	return nil
}

// SetUnsupported makes the System methods fail as on an image whose admin
// helper predates them.
func (f *Fake) SetUnsupported(u bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sys.Unsupported = u
}

// Counts returns how many reboots (including rollbacks) and shutdowns were
// asked for.
func (f *Fake) Counts() (reboots, shutdowns int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sys.Reboots, f.sys.Shutdowns
}
