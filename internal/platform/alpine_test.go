package platform

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/jancona/m17-web/internal/hat"
)

// newTestAlpine returns an Alpine whose files are in a temporary
// directory and whose helper is a script that answers like the real one
// and records what it was asked.
func newTestAlpine(t *testing.T) (*Alpine, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("m17-gateway.ini", `[General]
; a comment
Callsign=N1ADJ H
[Radio]
RXFrequency = 439500000
TXFrequency=434500000
Duplex=true
Power=12
[Reflector]
Name=M17-XOR
Module=B
[Modem]
Type=mmdvm
Port=/dev/ttyTEST
Speed=115200
`)
	write("hostname", "m17-hotspot\n")
	write("timezone", "America/New_York\n")
	write("M17Hosts.txt", "# hosts\nM17-M17 1.2.3.4 17000\nM17-XOR 5.6.7.8 17000\nbroken line\n")
	write("OverrideHosts.txt", "M17-M17 9.9.9.9 17001\nM17-MINE 10.0.0.1 17000\n")
	write("zone1970.tab", "# comment\nUS\t+404251-0740023\tAmerica/New_York\tEastern\nGB\t+513030-0000731\tEurope/London\n")
	write("helper", `#!/bin/sh
echo "$*" >> "`+dir+`/calls"
case "$1" in
	wifi-configured) printf 'country=GB\nssid=Home\nssid=Phone\n' ;;
	update-policy) printf 'AUTO_UPDATE=notify\nUPDATE_HOUR=4\n' ;;
	wifi-scan)
		[ -f "`+dir+`/scan-fails" ] && { echo "wpa_supplicant isn't running" >&2; exit 2; }
		printf -- '-48\t2437\t[WPA2-PSK-CCMP][ESS]\tHome\n-80\t2412\t[ESS]\tCafe Guest\n' ;;
	gateway) [ "$2" = status ] && exit 0; exit 0 ;;
	apply)
		cat > "`+dir+`/applied"
		echo "warning: invalid callsign 'X'"
		echo "reboot: false"
		echo "network: true"
		echo "gateway: true" ;;
	firmware-flash|firmware-status)
		[ -f "`+dir+`/fw-helper" ] || { echo "admin-helper: unknown command: $1" >&2; exit 2; }
		[ "$1" = firmware-status ] && printf 'state=running\nfile=mmdvm_hs/MMDVM_HS_Hat.bin\nstarted=1795047609\nlog: Flashing mmdvm_hs/MMDVM_HS_Hat.bin\nlog: Wrote and verified address 0x08000100 (1.25%%)\n'
		exit 0 ;;
	*) echo "unknown command" >&2; exit 2 ;;
esac
`)
	os.Chmod(filepath.Join(dir, "helper"), 0o755)

	a := NewAlpine()
	a.Helper = []string{filepath.Join(dir, "helper")}
	a.GatewayINI = filepath.Join(dir, "m17-gateway.ini")
	a.DashboardConfig = filepath.Join(dir, "config.php")
	a.HostFiles = []string{filepath.Join(dir, "M17Hosts.txt"), filepath.Join(dir, "OverrideHosts.txt")}
	a.ZoneTab = filepath.Join(dir, "zone1970.tab")
	a.HostnameFile = filepath.Join(dir, "hostname")
	a.TimeZoneFile = filepath.Join(dir, "timezone")
	a.ScanCache = filepath.Join(dir, "wifi-scan")
	a.Probe = func(port string) (hat.Result, error) {
		os.WriteFile(filepath.Join(dir, "probed"), []byte(port), 0o644)
		return hat.Result{Type: "mmdvm", Ident: "MMDVM_HS_Hat", Speed: 115200}, nil
	}
	return a, dir
}

func calls(t *testing.T, dir string) []string {
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestAlpineCurrent(t *testing.T) {
	a, _ := newTestAlpine(t)
	c, err := a.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Current{
		Callsign: "N1ADJ H", Hostname: "m17-hotspot", TimeZone: "America/New_York",
		WiFiCountry: "GB", WiFiSSIDs: []string{"Home", "Phone"},
		HATType: "mmdvm", RXFrequency: 439500000, TXFrequency: 434500000, Duplex: true, Power: 12,
		Reflector: "M17-XOR", Module: "B", AutoUpdate: "notify", UpdateHour: 4,
	}
	c.Online = false // depends on the test machine
	if c.Callsign != want.Callsign || c.Hostname != want.Hostname || c.TimeZone != want.TimeZone ||
		c.WiFiCountry != want.WiFiCountry || !slices.Equal(c.WiFiSSIDs, want.WiFiSSIDs) ||
		c.HATType != want.HATType || c.RXFrequency != want.RXFrequency || c.TXFrequency != want.TXFrequency ||
		c.Duplex != want.Duplex || c.Power != want.Power || c.Reflector != want.Reflector || c.Module != want.Module ||
		c.AutoUpdate != want.AutoUpdate || c.UpdateHour != want.UpdateHour {
		t.Errorf("Current:\n got %+v\nwant %+v", c, want)
	}
}

func TestAlpineCurrentPlaceholderAndDefaults(t *testing.T) {
	a, dir := newTestAlpine(t)
	os.WriteFile(a.GatewayINI, []byte("[General]\nCallsign=CALLSIGN_PLACEHOLDER\n[Modem]\nType=cc1200v2\n"), 0o644)
	os.Remove(filepath.Join(dir, "timezone"))
	a.Helper = []string{"/nonexistent/helper"} // helper unavailable: defaults
	c, err := a.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Callsign != "" || c.HATType != "cc1200" || c.TimeZone != "UTC" || c.AutoUpdate != "auto" || c.UpdateHour != 3 {
		t.Errorf("got %+v", c)
	}
}

func TestAlpineScanWiFi(t *testing.T) {
	a, dir := newTestAlpine(t)
	nets, err := a.ScanWiFi(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 || nets[0] != (Network{SSID: "Home", Signal: -48, Secured: true, Frequency: 2437}) ||
		nets[1].Secured || nets[1].SSID != "Cafe Guest" {
		t.Errorf("scan: %+v", nets)
	}
	// While the access point is up, the scan fails: use the saved list
	os.WriteFile(filepath.Join(dir, "scan-fails"), nil, 0o644)
	os.WriteFile(a.ScanCache, []byte("-60\t5180\t[WPA2-PSK-CCMP]\tSaved Net\n"), 0o644)
	nets, err = a.ScanWiFi(context.Background())
	if err != nil || len(nets) != 1 || nets[0].SSID != "Saved Net" {
		t.Errorf("from the cache: %+v, %v", nets, err)
	}
	os.Remove(a.ScanCache)
	if _, err := a.ScanWiFi(context.Background()); err == nil || !strings.Contains(err.Error(), "wpa_supplicant") {
		t.Errorf("no scan, no cache: %v", err)
	}
}

func TestAlpineDetectHATStopsAndRestartsGateway(t *testing.T) {
	a, dir := newTestAlpine(t)
	h, err := a.DetectHAT(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h != (HAT{Type: "mmdvm", Ident: "MMDVM_HS_Hat", Speed: 115200}) {
		t.Errorf("got %+v", h)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "probed")); string(b) != "/dev/ttyTEST" {
		t.Errorf("probed %q, want the INI's port", b)
	}
	got := calls(t, dir)
	if !slices.Equal(got, []string{"gateway status", "gateway stop", "gateway start"}) {
		t.Errorf("helper calls: %v", got)
	}
}

func TestAlpineDetectHATSkipsSX1255(t *testing.T) {
	a, dir := newTestAlpine(t)
	os.WriteFile(a.GatewayINI, []byte("[Modem]\nType=sx1255\n"), 0o644)
	h, err := a.DetectHAT(context.Background())
	if err != nil || h.Type != "" {
		t.Errorf("sx1255: %+v, %v", h, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "probed")); err == nil {
		t.Error("probed the serial port for an SX1255")
	}
}

func TestAlpineReflectorsAndZones(t *testing.T) {
	a, _ := newTestAlpine(t)
	refl, err := a.Reflectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Reflector{}
	for _, r := range refl {
		byName[r.Name] = r
	}
	if len(refl) != 3 || byName["M17-M17"].Server != "9.9.9.9" || byName["M17-MINE"].Port != 17000 {
		t.Errorf("reflectors (override wins): %+v", refl)
	}
	zones, err := a.TimeZones(context.Background())
	if err != nil || !slices.Equal(zones, []string{"America/New_York", "Europe/London", "UTC"}) {
		t.Errorf("zones: %v, %v", zones, err)
	}
}

func TestAlpinePasswordFromPHP(t *testing.T) {
	a, _ := newTestAlpine(t)
	if set, err := a.AdminPasswordSet(); set || err != nil {
		t.Errorf("no config.php: %v, %v", set, err)
	}
	// PHP's password_hash writes $2y$; Go writes $2a$; same algorithm
	h, _ := bcrypt.GenerateFromPassword([]byte("long-enough"), bcrypt.MinCost)
	phpHash := "$2y$" + string(h[4:])
	os.WriteFile(a.DashboardConfig, []byte("<?php\nreturn array (\n  'maxlines' => '15',\n  'admin_password_hash' => '"+phpHash+"',\n);\n"), 0o644)
	if set, _ := a.AdminPasswordSet(); !set {
		t.Error("hash not found")
	}
	if ok, err := a.CheckAdminPassword("long-enough"); !ok || err != nil {
		t.Errorf("right password: %v, %v", ok, err)
	}
	if ok, _ := a.CheckAdminPassword("wrong"); ok {
		t.Error("wrong password accepted")
	}
	os.WriteFile(a.DashboardConfig, []byte("<?php\nreturn array (\n  'admin_password_hash' => '',\n);\n"), 0o644)
	if set, _ := a.AdminPasswordSet(); set {
		t.Error("an empty hash counts as set")
	}
}

func TestAlpineApply(t *testing.T) {
	a, dir := newTestAlpine(t)
	res, err := a.Apply(context.Background(), []Setting{{Key: "callsign", Value: "X"}, {Key: "wifi_ssid", Value: "Home"}})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "applied")); string(b) != "callsign=X\nwifi_ssid=Home\n" {
		t.Errorf("helper got %q", b)
	}
	if !slices.Equal(res.Warnings, []string{"invalid callsign 'X'"}) || !res.NetworkChanged || res.RebootNeeded {
		t.Errorf("result: %+v", res)
	}
	if _, err := a.Apply(context.Background(), []Setting{{Key: "callsign", Value: "X\nuser_password=evil"}}); err == nil {
		t.Error("a value with a line break was passed on")
	}
}

func TestAlpineFirmware(t *testing.T) {
	a, dir := newTestAlpine(t)
	ctx := context.Background()

	// A helper from before firmware flashing
	if err := a.FlashFirmware(ctx, "mmdvm_hs/MMDVM_HS_Hat.bin"); err != ErrFlashUnsupported {
		t.Errorf("old helper: FlashFirmware = %v, want ErrFlashUnsupported", err)
	}
	if st, err := a.FlashStatus(ctx); err != nil || st.State != "" {
		t.Errorf("old helper: FlashStatus = %+v, %v", st, err)
	}

	os.WriteFile(filepath.Join(dir, "fw-helper"), nil, 0o644)
	if err := a.FlashFirmware(ctx, "mmdvm_hs/MMDVM_HS_Hat.bin"); err != nil {
		t.Fatal(err)
	}
	if c := calls(t, dir); c[len(c)-1] != "firmware-flash mmdvm_hs/MMDVM_HS_Hat.bin" {
		t.Errorf("helper called with %q", c[len(c)-1])
	}
	st, err := a.FlashStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running() || st.File != "mmdvm_hs/MMDVM_HS_Hat.bin" || st.Started != 1795047609 || len(st.Log) != 2 || !strings.Contains(st.Log[1], "1.25%") {
		t.Errorf("FlashStatus = %+v", st)
	}

	// Firmware files are read directly
	fwDir := filepath.Join(dir, "firmware", "cc1200")
	os.MkdirAll(fwDir, 0o755)
	os.WriteFile(filepath.Join(fwDir, "CC1200_HAT-fw.bin"), []byte("\x00CC1200-HAT 420-450 MHz\nFW v2.3 by Wojciech SP5WWP\x00"), 0o644)
	a.FirmwareDir = filepath.Join(dir, "firmware")
	imgs, err := a.FirmwareImages(ctx)
	if err != nil || len(imgs) != 1 || imgs[0].Ident != "CC1200-HAT 420-450 MHz / FW v2.3 by Wojciech SP5WWP" {
		t.Errorf("FirmwareImages = %+v, %v", imgs, err)
	}
}
