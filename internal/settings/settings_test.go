package settings

import (
	"math"
	"slices"
	"testing"

	"github.com/jancona/m17-web/internal/platform"
)

func good() Draft {
	return Draft{
		UseWiFi: true, WiFiSSID: "Home", WiFiPassword: "secret-pass", WiFiCountry: "US",
		Callsign: "N1ADJ", Suffix: "H", Hostname: "m17-hotspot", TimeZone: "America/New_York",
		HATType: "mmdvm", RXFrequency: 439.5, TXFrequency: 439.5, Power: 10, RFLevel: 100,
		Reflector: "M17-M17", Module: "C", AutoUpdate: "auto", UpdateHour: 3,
	}
}

var zones = []string{"America/New_York", "UTC"}

func TestChecksAcceptGoodDraft(t *testing.T) {
	d := good()
	for name, p := range map[string]Problems{
		"network":   d.CheckNetwork(CountryCodes()),
		"station":   d.CheckStation(zones),
		"hat":       d.CheckHAT(),
		"radio":     d.CheckRadio(),
		"reflector": d.CheckReflector([]platform.Reflector{{Name: "M17-M17"}}),
		"updates":   d.CheckUpdates(),
	} {
		if !p.OK() || len(p.Warnings) > 0 {
			t.Errorf("%s: errors %v, warnings %v", name, p.Errors, p.Warnings)
		}
	}
}

func TestChecksReject(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*Draft)
		check func(Draft) Problems
		field string
		warn  bool // a warning, not an error
	}{
		{"no ssid", func(d *Draft) { d.WiFiSSID = "" }, netCheck, "wifi_ssid", false},
		{"long ssid", func(d *Draft) { d.WiFiSSID = "123456789012345678901234567890123" }, netCheck, "wifi_ssid", false},
		{"short wifi password", func(d *Draft) { d.WiFiPassword = "1234567" }, netCheck, "wifi_password", false},
		{"bad country", func(d *Draft) { d.WiFiCountry = "XX" }, netCheck, "wifi_country", false},
		{"no callsign", func(d *Draft) { d.Callsign = "" }, stationCheck, "callsign", false},
		{"bad callsign chars", func(d *Draft) { d.Callsign = "N1*ADJ" }, stationCheck, "callsign", false},
		{"callsign too long", func(d *Draft) { d.Callsign = "AB1CDEFG" }, stationCheck, "callsign", false},
		{"callsign without digit", func(d *Draft) { d.Callsign = "NOCALL" }, stationCheck, "callsign", true},
		{"not a callsign the gateway takes", func(d *Draft) { d.Callsign = "NADJ" }, stationCheck, "callsign", false},
		{"mmdvm power", func(d *Draft) { d.RFLevel = 0 }, radioCheck, "rf_level", false},
		{"mmdvm power too high", func(d *Draft) { d.RFLevel = 101 }, radioCheck, "rf_level", false},
		{"two-letter suffix", func(d *Draft) { d.Suffix = "HS" }, stationCheck, "suffix", false},
		{"bad hostname", func(d *Draft) { d.Hostname = "-hotspot" }, stationCheck, "hostname", false},
		{"upper-case hostname", func(d *Draft) { d.Hostname = "Hotspot" }, stationCheck, "hostname", false},
		{"unknown zone", func(d *Draft) { d.TimeZone = "Mars/Olympus" }, stationCheck, "timezone", false},
		{"bad hat", func(d *Draft) { d.HATType = "rtl-sdr" }, func(d Draft) Problems { return d.CheckHAT() }, "hat_type", false},
		{"no frequency", func(d *Draft) { d.RXFrequency = math.NaN() }, radioCheck, "rx_mhz", false},
		{"frequency in Hz", func(d *Draft) { d.RXFrequency = 439500000 }, radioCheck, "rx_mhz", false},
		{"outside the CC1200 HAT's range", func(d *Draft) { d.HATType = "cc1200"; d.RXFrequency = 460.0 }, radioCheck, "rx_mhz", false},
		{"2 m on an SX1255", func(d *Draft) { d.HATType = "sx1255"; d.RXFrequency = 145.0 }, radioCheck, "rx_mhz", false},
		{"duplex same frequency", func(d *Draft) { d.Duplex = true }, radioCheck, "tx_mhz", false},
		{"cc1200 power", func(d *Draft) { d.HATType = "cc1200"; d.Power = 15 }, radioCheck, "power", false},
		{"unknown reflector", func(d *Draft) { d.Reflector = "M17-NOPE" }, reflCheck, "reflector", false},
		{"bad module", func(d *Draft) { d.Module = "c1" }, reflCheck, "module", false},
		{"bad policy", func(d *Draft) { d.AutoUpdate = "sometimes" }, func(d Draft) Problems { return d.CheckUpdates() }, "auto_update", false},
		{"bad hour", func(d *Draft) { d.UpdateHour = 24 }, func(d Draft) Problems { return d.CheckUpdates() }, "update_hour", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := good()
			c.edit(&d)
			p := c.check(d)
			if c.warn {
				if !p.OK() || p.Warnings[c.field] == "" {
					t.Errorf("want a warning on %s, got errors %v warnings %v", c.field, p.Errors, p.Warnings)
				}
				return
			}
			if p.Errors[c.field] == "" {
				t.Errorf("want an error on %s, got %v", c.field, p.Errors)
			}
		})
	}
}

func netCheck(d Draft) Problems     { return d.CheckNetwork(CountryCodes()) }
func stationCheck(d Draft) Problems { return d.CheckStation(zones) }
func radioCheck(d Draft) Problems   { return d.CheckRadio() }
func reflCheck(d Draft) Problems {
	return d.CheckReflector([]platform.Reflector{{Name: "M17-M17"}})
}

func TestEthernetNeedsNoWiFi(t *testing.T) {
	d := good()
	d.UseWiFi = false
	d.WiFiSSID = ""
	if p := d.CheckNetwork(CountryCodes()); !p.OK() {
		t.Errorf("Ethernet: %v", p.Errors)
	}
	for _, s := range d.Settings() {
		if s.Key == "wifi_ssid" || s.Key == "wifi_password" {
			t.Errorf("Ethernet draft produced %s", s.Key)
		}
	}
}

func TestSavedNetworkIsKept(t *testing.T) {
	d := FromCurrent(platform.Current{WiFiSSIDs: []string{"Home"}, WiFiCountry: "GB", Online: true})
	if !d.UseWiFi || d.WiFiSSID != "Home" || !d.KeepWiFi {
		t.Fatalf("from current: %+v", d)
	}
	d.UpdateKeepWiFi()
	got := d.Settings()
	if slices.ContainsFunc(got, func(s platform.Setting) bool { return s.Key == "wifi_ssid" || s.Key == "wifi_password" }) {
		t.Errorf("a saved network with no new password was rewritten: %v", got)
	}
	if !slices.Contains(got, platform.Setting{Key: "wifi_country", Value: "GB"}) {
		t.Errorf("country not kept: %v", got)
	}
	// A new password for the saved network: rewrite it
	d.WiFiPassword = "new-password"
	d.UpdateKeepWiFi()
	if d.KeepWiFi || !slices.Contains(d.Settings(), platform.Setting{Key: "wifi_password", Value: "new-password"}) {
		t.Errorf("new password for a saved network not applied: %+v", d)
	}
	// Another network with no password: an open network
	d.WiFiSSID, d.WiFiPassword = "Cafe", ""
	d.UpdateKeepWiFi()
	if d.KeepWiFi || !slices.Contains(d.Settings(), platform.Setting{Key: "wifi_ssid", Value: "Cafe"}) {
		t.Errorf("a new open network wasn't written: %+v", d)
	}
}

func TestOpenNetworkAndHexKey(t *testing.T) {
	d := good()
	d.WiFiPassword = ""
	if p := d.CheckNetwork(CountryCodes()); !p.OK() {
		t.Errorf("open network: %v", p.Errors)
	}
	d.WiFiPassword = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if p := d.CheckNetwork(CountryCodes()); !p.OK() {
		t.Errorf("64-digit hex key: %v", p.Errors)
	}
}

func TestSettings(t *testing.T) {
	d := good()
	got := d.Settings()
	want := []platform.Setting{
		{Key: "wifi_country", Value: "US"},
		{Key: "wifi_ssid", Value: "Home"},
		{Key: "wifi_password", Value: "secret-pass"},
		{Key: "hostname", Value: "m17-hotspot"},
		{Key: "timezone", Value: "America/New_York"},
		{Key: "callsign", Value: "N1ADJ H"},
		{Key: "hat", Value: "mmdvm"},
		{Key: "frequency", Value: "439.5"},
		{Key: "duplex", Value: "false"},
		{Key: "power", Value: "10"},
		{Key: "rf_level", Value: "100"}, // RFLevelWas 0: a change
		{Key: "reflector", Value: "M17-M17"},
		{Key: "module", Value: "C"},
		{Key: "auto_update", Value: "auto"},
		{Key: "update_hour", Value: "3"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Settings:\n got %v\nwant %v", got, want)
	}

	d.Duplex, d.TXFrequency = true, 434.5
	d.Reflector = ""
	got = d.Settings()
	for _, w := range []platform.Setting{
		{Key: "rx_frequency", Value: "439.5"}, {Key: "tx_frequency", Value: "434.5"},
		{Key: "duplex", Value: "true"}, {Key: "reflector", Value: "none"},
	} {
		if !slices.Contains(got, w) {
			t.Errorf("duplex, RF only: missing %v in %v", w, got)
		}
	}
	if slices.ContainsFunc(got, func(s platform.Setting) bool { return s.Key == "module" }) {
		t.Errorf("RF only still sets module: %v", got)
	}
}

func TestParseMHz(t *testing.T) {
	for in, want := range map[string]float64{"439.5": 439.5, " 439,500 ": 439.5, "144": 144} {
		if got := ParseMHz(in); got != want {
			t.Errorf("ParseMHz(%q) = %v, want %v", in, got, want)
		}
	}
	if !math.IsNaN(ParseMHz("abc")) {
		t.Error("ParseMHz(abc) is not NaN")
	}
}

func TestAdminPassword(t *testing.T) {
	if CheckAdminPassword("short", "short").OK() {
		t.Error("short password accepted")
	}
	if CheckAdminPassword("long-enough", "different").OK() {
		t.Error("mismatched confirmation accepted")
	}
	if p := CheckAdminPassword("long-enough", "long-enough"); !p.OK() {
		t.Errorf("good password: %v", p.Errors)
	}
}

func TestFromCurrent(t *testing.T) {
	d := FromCurrent(platform.Current{Callsign: "N1ADJ H", RXFrequency: 439_500_000, Online: true})
	if d.Callsign != "N1ADJ" || d.Suffix != "H" {
		t.Errorf("callsign split: %q %q", d.Callsign, d.Suffix)
	}
	if d.RXFrequency != 439.5 {
		t.Errorf("frequency: %v", d.RXFrequency)
	}
	if d.UseWiFi {
		t.Error("online with no WiFi configured should default to Ethernet")
	}
	if d.WiFiCountry != "US" || d.AutoUpdate != "auto" {
		t.Errorf("defaults: %q %q", d.WiFiCountry, d.AutoUpdate)
	}
}

func TestSSH(t *testing.T) {
	d := good()
	if p := d.CheckSSH(""); !p.OK() || p.Warnings["ssh_key"] == "" {
		t.Errorf("no key, no passwords: want only a warning, got %v", p)
	}
	d.SSHKey = NormalizeSSHKey(" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb0Xv3mQ0x0aP1l2n3o4p5\nq6r7s8t9u0v1w2x3y4z5A me@pc\r\n")
	if p := d.CheckSSH(""); !p.OK() || len(p.Warnings) != 0 {
		t.Errorf("a key wrapped across lines: %v", p)
	}
	for _, bad := range []string{"AAAAC3Nza", "ssh-ed25519", "ssh-dss AAAAB3NzaC1kc3M= old"} {
		d.SSHKey = bad
		if d.CheckSSH("").OK() {
			t.Errorf("accepted key %q", bad)
		}
	}
	d.SSHKey = ""
	d.SSHPasswordWas, d.SSHPasswordLogins = true, true
	if !d.CheckSSH("").OK() {
		t.Error("already allowed: a new password shouldn't be required")
	}
	if slices.ContainsFunc(d.Settings(), func(s platform.Setting) bool { return s.Key == "enable_ssh_password" }) {
		t.Error("enable_ssh_password sent without a change")
	}
	d.SSHPasswordLogins = false
	if !slices.Contains(d.Settings(), platform.Setting{Key: "enable_ssh_password", Value: "false"}) {
		t.Error("turning password logins off isn't sent")
	}
	d.UserPassword = "short"
	if d.CheckSSH("short").OK() {
		t.Error("short password accepted")
	}
	d.UserPassword = "long-enough"
	if d.CheckSSH("different").Errors["user_confirm"] == "" {
		t.Error("mismatched confirmation accepted")
	}
}

// Only the CC1200 HAT uses the power setting, but m17-gateway checks it
// whatever the HAT, so an out-of-range value is replaced for the others.
func TestPowerForOtherHATs(t *testing.T) {
	power := func(d Draft) string {
		for _, s := range d.Settings() {
			if s.Key == "power" {
				return s.Value
			}
		}
		return ""
	}
	d := good()
	d.Power = 30
	if p := d.CheckRadio(); !p.OK() {
		t.Errorf("MMDVM with Power 30: %v", p.Errors)
	}
	if got := power(d); got != "10" {
		t.Errorf("MMDVM with Power 30 writes power=%s, want 10", got)
	}
	d.Power = -5
	if got := power(d); got != "-5" {
		t.Errorf("an in-range power is kept: got %s", got)
	}
	d.HATType, d.Power = "cc1200", 14
	if p := d.CheckRadio(); !p.OK() || power(d) != "14" {
		t.Errorf("CC1200 at 14 dBm: %v, %s", p.Errors, power(d))
	}
}

func TestFrequencyPerHAT(t *testing.T) {
	for _, tc := range []struct {
		hat       string
		mhz       float64
		err, warn bool
	}{
		{"cc1200", 439.5, false, false},
		{"cc1200", 450, false, false},
		{"cc1200", 460, true, false},
		{"cc1200", 145.5, true, false},
		{"sx1255", 400, false, true}, // outside the amateur bands
		{"sx1255", 460, false, true}, // outside the amateur bands
		{"sx1255", 510.1, true, false},
		{"sx1255", 145.5, true, false},
		{"mmdvm", 145.5, false, false},
		{"mmdvm", 222.5, false, false},
		{"mmdvm", 915, false, false},
		{"mmdvm", 50.5, false, true}, // MMDVM_HS refuses it; repeater boards don't
		{"mmdvm", 1296, false, true},
	} {
		d := good()
		d.HATType, d.RXFrequency, d.Duplex = tc.hat, tc.mhz, false
		p := d.CheckRadio()
		if (p.Errors["rx_mhz"] != "") != tc.err || (p.Warnings["rx_mhz"] != "") != tc.warn {
			t.Errorf("%s at %g MHz: errors %v, warnings %v", tc.hat, tc.mhz, p.Errors, p.Warnings)
		}
	}
}

func TestRFLevel(t *testing.T) {
	has := func(d Draft) (string, bool) {
		for _, s := range d.Settings() {
			if s.Key == "rf_level" {
				return s.Value, true
			}
		}
		return "", false
	}
	d := good() // an MMDVM board, nothing detected
	d.RFLevel = 25
	if v, ok := has(d); !ok || v != "25" {
		t.Errorf("MMDVM hotspot: rf_level %q, %v", v, ok)
	}
	// A repeater board: no field, nothing written, no error
	d.SetDetected(platform.HAT{Type: "mmdvm", Ident: "MMDVM RB_STM32_DVM(446) 20251014 12.0000 MHz GitID #9267a90"})
	if d.UsesRFLevel() {
		t.Error("repeater board offered RF level")
	}
	d.RFLevel = 0
	if _, ok := has(d); ok || !d.CheckRadio().OK() {
		t.Errorf("repeater board: rf_level written or refused: %v", d.CheckRadio().Errors)
	}
	d.SetDetected(platform.HAT{Type: "mmdvm", Ident: "MMDVM_HS_Dual_Hat-v1.6.1 20261116 14.7456MHz dual ADF7021 FW by CA6JAU (M17) GitID #02e5646"})
	if !d.UsesRFLevel() {
		t.Error("MMDVM_HS board not offered RF level")
	}
	// Unchanged: not written
	d.RFLevel, d.RFLevelWas = 60, 60
	if _, ok := has(d); ok {
		t.Error("an unchanged RF level was written")
	}
	// A draft saved before the setting existed (0) writes nothing
	if _, ok := has(d); ok {
		t.Error("RF level 0 written")
	}
	for _, hat := range []string{"cc1200", "sx1255"} {
		d.HATType, d.RFLevel = hat, 50
		if _, ok := has(d); ok || d.UsesRFLevel() {
			t.Errorf("%s: RF level used", hat)
		}
	}
}
