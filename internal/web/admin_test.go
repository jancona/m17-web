package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jancona/m17-web/internal/platform"
)

// postFrom submits form to action with the CSRF token from the page at
// page (admin forms post to /admin/<page>/<section>).
func (h *harness) postFrom(c *http.Client, page, action string, form url.Values) (int, string, string) {
	h.t.Helper()
	_, _, body := h.get(c, page)
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatalf("no CSRF token on %s:\n%s", page, body)
	}
	form.Set("csrf", m[1])
	return h.postRaw(c, action, form)
}

// lastApplied is the keys of the last Apply.
func (h *harness) lastApplied() []string {
	h.t.Helper()
	if len(h.fake.Applied) == 0 {
		h.t.Fatal("nothing applied")
	}
	var keys []string
	for _, s := range h.fake.Applied[len(h.fake.Applied)-1] {
		keys = append(keys, s.Key+"="+s.Value)
	}
	return keys
}

// TestAdminAfterSetup: once set up, the hotspot opens on the admin pages,
// and every admin page renders.
func TestAdminAfterSetup(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	code, loc, _ := h.get(h.client, "/setup/")
	h.expectRedirect(code, loc, "/admin/")
	code, loc, _ = h.get(h.client, "/admin/")
	h.expectRedirect(code, loc, "/admin/station")

	for _, p := range adminPages {
		code, _, body := h.get(h.client, "/admin/"+p.ID)
		if code != 200 {
			t.Fatalf("/admin/%s: %d\n%s", p.ID, code, body)
		}
		for _, s := range p.Sections {
			if !s.Panel && !strings.Contains(body, `action="/admin/`+p.ID+`/`+s.ID+`"`) {
				t.Errorf("/admin/%s has no form for %s", p.ID, s.ID)
			}
		}
	}
	if code, _, _ := h.get(h.client, "/admin/nope"); code != 404 {
		t.Errorf("unknown admin page: %d", code)
	}

	// Logged out: the admin pages need a login
	other := h.newClient()
	code, loc, _ = h.get(other, "/admin/station")
	h.expectRedirect(code, loc, "/setup/login")
}

// TestAdminSaveAppliesOnlyItsSection: Save applies the section's own keys,
// and the page after says so.
func TestAdminSaveAppliesOnlyItsSection(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	code, loc, _ := h.postFrom(h.client, "/admin/station", "/admin/station/callsign", url.Values{"callsign": {"w1aw"}, "suffix": {""}})
	h.expectRedirect(code, loc, "/admin/station#callsign")
	if got := h.lastApplied(); !slices.Equal(got, []string{"callsign=W1AW"}) {
		t.Errorf("applied %v, want only the callsign", got)
	}
	_, _, body := h.get(h.client, "/admin/station")
	if !strings.Contains(body, "Saved.") || !strings.Contains(body, `value="W1AW"`) {
		t.Errorf("after saving, the page doesn't show it:\n%s", body)
	}
	// The notice is shown once
	if _, _, body = h.get(h.client, "/admin/station"); strings.Contains(body, "Saved.") {
		t.Error("the notice is shown again")
	}

	h.postFrom(h.client, "/admin/station", "/admin/station/reflector", url.Values{"reflector": {"M17-USA"}, "module": {"c"}})
	if got := h.lastApplied(); !slices.Equal(got, []string{"reflector=M17-USA", "module=C"}) {
		t.Errorf("applied %v, want the reflector and module", got)
	}

	h.postFrom(h.client, "/admin/updates", "/admin/updates/updates", url.Values{"auto_update": {"off"}, "update_hour": {"2"}})
	if got := h.lastApplied(); !slices.Equal(got, []string{"auto_update=off", "update_hour=2"}) {
		t.Errorf("applied %v, want the update policy", got)
	}
}

// TestAdminValidation: an error re-shows the page with what was typed,
// and applies nothing.
func TestAdminValidation(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	n := len(h.fake.Applied)

	code, _, body := h.postFrom(h.client, "/admin/radio", "/admin/radio/frequency", url.Values{"rx_mhz": {"4x5"}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad frequency: %d", code)
	}
	if !strings.Contains(body, "Not saved") || !strings.Contains(body, "Enter the frequency in MHz") {
		t.Errorf("the error page doesn't show the problem:\n%s", body)
	}
	if len(h.fake.Applied) != n {
		t.Error("an invalid section was applied")
	}
}

// TestAdminPassword: changing the admin password needs the current one.
func TestAdminPassword(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	code, _, body := h.postFrom(h.client, "/admin/security", "/admin/security/adminpw",
		url.Values{"current": {"wrong-one"}, "password": {"new-password"}, "confirm": {"new-password"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "current admin password") {
		t.Fatalf("wrong current password: %d", code)
	}
	if ok, _ := h.fake.CheckAdminPassword("new-password"); ok {
		t.Fatal("the password changed without the current one")
	}
	code, loc, _ := h.postFrom(h.client, "/admin/security", "/admin/security/adminpw",
		url.Values{"current": {"long-enough"}, "password": {"new-password"}, "confirm": {"new-password"}})
	h.expectRedirect(code, loc, "/admin/security#adminpw")
	if ok, _ := h.fake.CheckAdminPassword("new-password"); !ok {
		t.Error("the password didn't change")
	}
}

// TestAdminDetectHAT: Detect asks the HAT and shows the answer; saving the
// HAT then uses the speed it answered at.
func TestAdminDetectHAT(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	h.fake.SetHAT(platform.HAT{Type: "mmdvm", Ident: "MMDVM RB_STM32_DVM(446) 20260424", Speed: 460800})

	code, loc, _ := h.postFrom(h.client, "/admin/radio", "/admin/radio/hat", url.Values{"action": {"detect"}})
	h.expectRedirect(code, loc, "/admin/radio#hat")
	_, _, body := h.get(h.client, "/admin/radio")
	if !strings.Contains(body, "RB_STM32_DVM(446)") {
		t.Errorf("the detected HAT isn't shown:\n%s", body)
	}
	h.postFrom(h.client, "/admin/radio", "/admin/radio/hat", url.Values{"hat_type": {"mmdvm"}})
	if got := h.lastApplied(); !slices.Equal(got, []string{"hat=mmdvm", "modem_speed=460800"}) {
		t.Errorf("applied %v, want the HAT with its detected speed", got)
	}
}

// TestRerunSetup: Run setup again opens the wizard with every step
// reachable, starting from the current settings.
func TestRerunSetup(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	code, loc, _ := h.postFrom(h.client, "/admin/system", "/admin/system/rerun", url.Values{})
	h.expectRedirect(code, loc, "/setup/")
	code, loc, _ = h.get(h.client, "/setup/")
	h.expectRedirect(code, loc, "/setup/password")
	code, _, body := h.get(h.client, "/setup/ssh")
	if code != 200 {
		t.Fatalf("a later step isn't reachable when setup is run again: %d", code)
	}
	if !strings.Contains(body, `href="/setup/reflector"`) || !strings.Contains(body, "choose any step") {
		t.Error("the step indicator doesn't link every step")
	}
	_, _, body = h.get(h.client, "/setup/station")
	if !strings.Contains(body, `value="N1ADJ"`) {
		t.Error("the re-run doesn't start from the current settings")
	}
}

// TestAdminWiFiNetworks: saved networks are listed, added (keeping the
// others) and removed.
func TestAdminWiFiNetworks(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	_, _, body := h.get(h.client, "/admin/network")
	if !strings.Contains(body, "Shack") || !strings.Contains(body, "connected") {
		t.Fatalf("saved networks not listed:\n%s", body)
	}
	code, loc, _ := h.postFrom(h.client, "/admin/network", "/admin/network/wifinets",
		url.Values{"action": {"add"}, "wifi_ssid": {"-other-"}, "wifi_ssid_other": {"Field Day"}, "wifi_password": {"fd-secret-1"}})
	h.expectRedirect(code, loc, "/admin/network#wifinets")
	saved, _ := h.fake.SavedWiFi(t.Context())
	if len(saved) != 3 || saved[0].SSID != "Field Day" || saved[0].Open {
		t.Errorf("after adding: %+v", saved)
	}
	// A short password is refused, and nothing is added
	code, _, body = h.postFrom(h.client, "/admin/network", "/admin/network/wifinets",
		url.Values{"action": {"add"}, "wifi_ssid": {"-other-"}, "wifi_ssid_other": {"Short"}, "wifi_password": {"abc"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "8 to 63") || !strings.Contains(body, `value="Short"`) {
		t.Errorf("short password: %d", code)
	}
	code, loc, _ = h.postFrom(h.client, "/admin/network", "/admin/network/wifinets", url.Values{"action": {"remove"}, "ssid": {"Shack"}})
	h.expectRedirect(code, loc, "/admin/network#wifinets")
	saved, _ = h.fake.SavedWiFi(t.Context())
	if len(saved) != 2 || slices.ContainsFunc(saved, func(n platform.SavedNetwork) bool { return n.SSID == "Shack" }) {
		t.Errorf("after removing: %+v", saved)
	}
}

// TestAdminSSHKeys: the keys added by the wizard are listed with their
// fingerprints, and can be removed.
func TestAdminSSHKeys(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	keys, _ := h.fake.SSHKeys(t.Context())
	if len(keys) != 1 {
		t.Fatalf("the wizard's key: %+v", keys)
	}
	_, _, body := h.get(h.client, "/admin/security")
	if !strings.Contains(body, "jim@mac") || !strings.Contains(body, keys[0].Fingerprint()) {
		t.Fatalf("key not listed:\n%s", body)
	}
	code, loc, _ := h.postFrom(h.client, "/admin/security", "/admin/security/sshkeys", url.Values{"action": {"remove"}, "key": {keys[0].Key}})
	h.expectRedirect(code, loc, "/admin/security#sshkeys")
	if keys, _ := h.fake.SSHKeys(t.Context()); len(keys) != 0 {
		t.Errorf("key not removed: %+v", keys)
	}
}

// TestAdminOSUpdate: check, install, then restart.
func TestAdminOSUpdate(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	_, _, body := h.get(h.client, "/admin/updates")
	if !strings.Contains(body, "2026.11.23.1") || !strings.Contains(body, "Go back to 2026.11.20.1") {
		t.Fatalf("OS status not shown:\n%s", body)
	}
	code, loc, _ := h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {"check"}})
	h.expectRedirect(code, loc, "/admin/updates#os")
	if _, _, body = h.get(h.client, "/admin/updates"); !strings.Contains(body, "Update available: 2026.11.25.1") {
		t.Fatalf("check result not shown:\n%s", body)
	}
	h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {"install"}})
	if _, _, body = h.get(h.client, "/admin/updates"); !strings.Contains(body, "The update is installed") {
		t.Fatalf("install result not shown:\n%s", body)
	}
	code, _, body = h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {"reboot"}})
	if code != 200 || !strings.Contains(body, "Restarting") {
		t.Errorf("restart: %d", code)
	}
	if r, _ := h.fake.Counts(); r != 1 {
		t.Errorf("reboots: %d", r)
	}
}

// TestAdminSystemAndLogs: the system panel, shut down, and each log.
func TestAdminSystemAndLogs(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	_, _, body := h.get(h.client, "/admin/system")
	for _, want := range []string{"Raspberry Pi Zero 2 W", "MMDVM_HS_Dual_Hat", "°C", "free of"} {
		if !strings.Contains(body, want) {
			t.Errorf("system panel lacks %q", want)
		}
	}
	code, _, body := h.postFrom(h.client, "/admin/system", "/admin/system/sysinfo", url.Values{"action": {"shutdown"}})
	if code != 200 || !strings.Contains(body, "shutting down") {
		t.Errorf("shutdown: %d", code)
	}
	if _, s := h.fake.Counts(); s != 1 {
		t.Errorf("shutdowns: %d", s)
	}
	for _, name := range platform.LogNames {
		code, _, body := h.get(h.client, "/admin/logs?log="+name)
		if code != 200 || !strings.Contains(body, `aria-current="page">`+logTitles[name]) {
			t.Errorf("log %s: %d", name, code)
		}
	}
	if _, _, body = h.get(h.client, "/admin/logs?log=../../etc/passwd"); !strings.Contains(body, "health-check") {
		t.Error("an unknown log name doesn't fall back to the events log")
	}
}

// TestAdminOldSystem: on an image whose admin helper predates the panels,
// they say so instead of failing.
func TestAdminOldSystem(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	h.fake.SetUnsupported(true)
	for _, p := range []string{"/admin/network", "/admin/security", "/admin/updates", "/admin/logs?log=system"} {
		code, _, body := h.get(h.client, p)
		if code != 200 || !strings.Contains(body, "This system version can") {
			t.Errorf("%s on an old system: %d", p, code)
		}
	}
}

// TestAdminFirmware: the Firmware page uses the gateway's report of the
// HAT, flashes, and after the flash uses the gateway's new report.
func TestAdminFirmware(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client) // MMDVM_HS_Dual_Hat with old firmware

	_, _, body := h.get(h.client, "/admin/firmware")
	if !strings.Contains(body, "MMDVM_HS_Dual_Hat-v1.5.2") || !strings.Contains(body, "the gateway") {
		t.Fatalf("the gateway's report isn't used:\n%s", body)
	}
	if !strings.Contains(body, "Newer firmware is available") || !strings.Contains(body, `value="mmdvm_hs/MMDVM_HS_Dual_Hat.bin" checked`) {
		t.Fatalf("the update isn't offered:\n%s", body)
	}
	// The Radio page shows the same report without probing
	if _, _, body = h.get(h.client, "/admin/radio"); !strings.Contains(body, "The gateway found an MMDVM HAT") {
		t.Errorf("the Radio page doesn't show the gateway's report")
	}

	code, _, body := h.postFrom(h.client, "/admin/firmware", "/admin/firmware/firmware", url.Values{"action": {"flash"}, "firmware": {"nope.bin"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "isn&#39;t one of the firmware files") {
		t.Errorf("unknown file: %d", code)
	}
	code, loc, _ := h.postFrom(h.client, "/admin/firmware", "/admin/firmware/firmware", url.Values{"action": {"flash"}, "firmware": {"mmdvm_hs/MMDVM_HS_Dual_Hat.bin"}})
	h.expectRedirect(code, loc, "/admin/firmware")
	_, _, body = h.get(h.client, "/admin/firmware")
	if !strings.Contains(body, "Firmware written") || !strings.Contains(body, "newest firmware available here") {
		t.Errorf("after flashing, the page doesn't show the new firmware:\n%s", body)
	}
}

// TestAdminFirmwareFlashRunning: a flash in progress is shown, and the
// page refreshes until it ends.
func TestAdminFirmwareFlashRunning(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	h.fake.FlashTime = time.Hour // still running
	h.postFrom(h.client, "/admin/firmware", "/admin/firmware/firmware", url.Values{"action": {"flash"}, "firmware": {"mmdvm_hs/MMDVM_HS_Dual_Hat.bin"}})
	_, _, body := h.get(h.client, "/admin/firmware")
	if !strings.Contains(body, "Flashing mmdvm_hs/MMDVM_HS_Dual_Hat.bin") || !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("a running flash isn't shown:\n%s", body)
	}
}

// TestAdminFirmwareOtherHAT: when the gateway found another type of HAT
// than the one configured, the Firmware page says so.
func TestAdminFirmwareOtherHAT(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	h.postFrom(h.client, "/admin/radio", "/admin/radio/hat", url.Values{"hat_type": {"cc1200"}})
	_, _, body := h.get(h.client, "/admin/firmware")
	if !strings.Contains(body, "The gateway found an MMDVM HAT, but the hotspot is set up for a CC1200 HAT") {
		t.Errorf("the mismatch isn't explained:\n%s", body)
	}
}
