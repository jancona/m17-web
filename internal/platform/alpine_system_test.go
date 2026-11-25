package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOSStatus(t *testing.T) {
	out := `{"boot_slot":"A","active_slot":"A","previous_slot":"B","pending":false,"boot_tries":0,"slot_a_version":"2026.11.23.1","slot_b_version":"2026.11.20.1","running_version":"2026.11.23.1"}
update.TIME=2026-11-24 03:02:40 EST
update.RESULT=installed
`
	st := parseOSStatus(out)
	if st.Running != "2026.11.23.1" || st.ActiveSlot != "A" || st.PreviousVersion != "2026.11.20.1" || st.LastUpdate["RESULT"] != "installed" {
		t.Errorf("parsed %+v", st)
	}
	if st := parseOSStatus(`{"active_slot":"A","previous_slot":""}`); st.PreviousVersion != "" {
		t.Errorf("no previous slot: %+v", st)
	}
}

func TestSSHKeyFingerprint(t *testing.T) {
	// ssh-keygen -lf of this key
	k := SSHKey{Type: "ssh-ed25519", Key: "AAAAC3NzaC1lZDI1NTE5AAAAIGb0Xv3mQ0x0aP1l2n3o4p5q6r7s8t9u0v1w2x3y4z5A"}
	if fp := k.Fingerprint(); !strings.HasPrefix(fp, "SHA256:") || len(fp) != 50 {
		t.Errorf("fingerprint %q", fp)
	}
	if fp := (SSHKey{Key: "not base64!"}).Fingerprint(); fp != "" {
		t.Errorf("bad key fingerprint %q", fp)
	}
}

func TestSystemInfoFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dash := write("dashboard.log", `{"time":"2026-11-24T03:02:45-05:00","type":"Gateway","subtype":"Start","modem":"cc1200","modemIdent":"old"}
{"time":"2026-11-24T10:00:43-05:00","type":"Gateway","subtype":"Start","modem":"mmdvm","modemIdent":"MMDVM RB_STM32_DVM(446) 20260424"}
{"time":"2026-11-24T10:00:48-05:00","type":"Reflector","subtype":"Connect","name":"M17-M17","module":"T"}
`)
	a := &Alpine{
		GatewayINI:   write("gw.ini", "[General]\nDashboardLog="+dash+"\n"),
		HostnameFile: write("hostname", "m17-repeater\n"),
		Files: SystemFiles{
			Model:     write("model", "Raspberry Pi 3 Model B Plus Rev 1.4\x00"),
			OSRelease: write("release", "m17-hotspot-os\nVersion: 2026.11.23.1\n"),
			Kernel:    write("kernel", "6.18.60+rpt-rpi-v8\n"),
			Uptime:    write("uptime", "24872.13 50671.16\n"),
			Temp:      write("temp", "54230\n"),
			MemInfo:   write("meminfo", "MemTotal:         474080 kB\nMemFree:          100 kB\nMemAvailable:     391000 kB\n"),
			LoadAvg:   write("loadavg", "0.93 0.33 0.12 1/133 2055\n"),
			DataDir:   dir,
		},
	}
	si, err := a.SystemInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if si.Board != "Raspberry Pi 3 Model B Plus Rev 1.4" || si.Hostname != "m17-repeater" || si.OSVersion != "2026.11.23.1" {
		t.Errorf("names: %+v", si)
	}
	if si.Uptime.Hours() < 6.9 || si.TempC != 54.23 || si.MemTotal != 474080*1024 || si.MemAvailable != 391000*1024 || si.Load != "0.93 0.33 0.12" {
		t.Errorf("figures: %+v", si)
	}
	if si.Modem != "mmdvm" || si.ModemIdent != "MMDVM RB_STM32_DVM(446) 20260424" {
		t.Errorf("modem from the last Gateway Start: %q %q", si.Modem, si.ModemIdent)
	}
}

func TestTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	var b strings.Builder
	for i := range 1000 {
		b.WriteString(strings.Repeat("x", i%7) + "\n")
	}
	os.WriteFile(p, []byte(b.String()), 0o644)
	if got := tail(p, 10); len(got) != 10 {
		t.Errorf("tail 10 gave %d lines", len(got))
	}
	if got := tail(filepath.Join(t.TempDir(), "none"), 10); got != nil {
		t.Errorf("missing file: %v", got)
	}
}
