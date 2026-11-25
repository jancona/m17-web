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

// TestAdminGateway: the gateway's state on the System page, and stop,
// start and restart.
func TestAdminGateway(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)

	_, _, body := h.get(h.client, "/admin/system")
	for _, want := range []string{"running for", "starts with the hotspot", `href="/admin/logs?log=gateway"`,
		`value="stop"`, `value="restart"`} {
		if !strings.Contains(body, want) {
			t.Errorf("gateway section lacks %q", want)
		}
	}
	if strings.Contains(body, `name="action" value="start"`) {
		t.Error("Start offered while the gateway runs")
	}
	code, loc, _ := h.postFrom(h.client, "/admin/system", "/admin/system/gateway", url.Values{"action": {"stop"}})
	h.expectRedirect(code, loc, "/admin/system#gateway")
	_, _, body = h.get(h.client, "/admin/system")
	for _, want := range []string{"<strong>stopped</strong>", "off the air", `name="action" value="start"`, "The gateway is stopped."} {
		if !strings.Contains(body, want) {
			t.Errorf("stopped gateway section lacks %q", want)
		}
	}
	for _, action := range []string{"start", "restart"} {
		code, loc, _ = h.postFrom(h.client, "/admin/system", "/admin/system/gateway", url.Values{"action": {action}})
		h.expectRedirect(code, loc, "/admin/system#gateway")
	}
	if got, want := h.fake.GatewayCalls(), []string{"stop", "start", "restart"}; !slices.Equal(got, want) {
		t.Errorf("gateway actions %v, want %v", got, want)
	}
	// Anything else is refused
	if code, _, _ := h.postFrom(h.client, "/admin/system", "/admin/system/gateway", url.Values{"action": {"reload"}}); code != http.StatusBadRequest {
		t.Errorf("unknown action: %d", code)
	}
}

// TestAdminExpert: the gateway's settings file: checked by the gateway,
// refused with its errors (keeping what was typed), saved, and restored.
func TestAdminExpert(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	post := func(action, text string) (int, string, string) {
		t.Helper()
		return h.postFrom(h.client, "/admin/expert", "/admin/expert/gatewayini", url.Values{"action": {action}, "ini": {text}})
	}
	orig, _ := h.fake.GatewayConfig(t.Context())

	_, _, body := h.get(h.client, "/admin/expert")
	if !strings.Contains(body, "Callsign=N1ADJ") || !strings.Contains(body, `name="ini"`) || strings.Contains(body, "Go back to the previous file") {
		t.Fatalf("expert page:\n%s", body)
	}

	bad := strings.Replace(orig.Text, "Callsign=N1ADJ", "Callsign=BAD", 1) + "<script>x</script>\n"
	code, _, body := post("check", bad)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "bad callsign BAD") || !strings.Contains(body, "wouldn&#39;t start") {
		t.Errorf("check, bad: %d\n%s", code, body)
	}
	if !strings.Contains(body, "Callsign=BAD") || !strings.Contains(body, "&lt;script&gt;x&lt;/script&gt;") {
		t.Error("the typed text isn't kept, escaped")
	}
	code, _, body = post("save", bad)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Not saved") || !strings.Contains(body, "bad callsign BAD") {
		t.Errorf("save, bad: %d", code)
	}
	if gc, _ := h.fake.GatewayConfig(t.Context()); gc.Text != orig.Text {
		t.Error("a bad file was saved")
	}

	good := strings.ReplaceAll(orig.Text+"Typo=1\n", "\n", "\r\n")
	code, _, body = post("check", good)
	if code != 200 || !strings.Contains(body, "would start") || !strings.Contains(body, "Typo is ignored") {
		t.Errorf("check, good: %d", code)
	}
	code, loc, _ := post("save", good)
	h.expectRedirect(code, loc, "/admin/expert#gatewayini")
	gc, _ := h.fake.GatewayConfig(t.Context())
	if gc.Text != orig.Text+"Typo=1\n" || !gc.HasPrevious {
		t.Errorf("saved %q (previous kept: %v), want LF line ends", gc.Text, gc.HasPrevious)
	}
	_, _, body = h.get(h.client, "/admin/expert")
	for _, want := range []string{"Saved, and the gateway is restarting", "Typo is ignored", "Go back to the previous file"} {
		if !strings.Contains(body, want) {
			t.Errorf("after saving, no %q", want)
		}
	}
	code, loc, _ = h.postFrom(h.client, "/admin/expert", "/admin/expert/gatewayini", url.Values{"action": {"restore"}})
	h.expectRedirect(code, loc, "/admin/expert#gatewayini")
	if gc, _ := h.fake.GatewayConfig(t.Context()); gc.Text != orig.Text {
		t.Errorf("restored %q", gc.Text)
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

func TestPackagesResult(t *testing.T) {
	for log, want := range map[string]string{
		"updated packages: m17-web 0_git2-r0":                    "Updated: m17-web 0_git2-r0",
		"The M17 packages are up to date":                        "The M17 software is up to date.",
		"package updates not installed: the gateway stayed busy": "Not updated: the gateway stayed busy. Try again when nobody is transmitting.",
	} {
		if got := packagesResult(platform.JobStatus{State: "ok", Log: []string{log}}); got != want {
			t.Errorf("%q: %q, want %q", log, got, want)
		}
	}
}

// TestRefreshWhileRunning: a page showing a running job reloads itself
// with a URL of its own, so it reloads even when reached as
// /admin/updates#os (a refresh to the same URL would only jump to the
// fragment).
func TestRefreshWhileRunning(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	h.fake.SetJob("os-check", platform.JobStatus{State: "running", Started: time.Now().Unix()})
	_, _, body := h.get(h.client, "/admin/updates")
	if !strings.Contains(body, `<meta http-equiv="refresh" content="3;url=/admin/updates">`) {
		t.Errorf("no refresh with its own URL:\n%s", body[:min(len(body), 600)])
	}
	h.fake.SetJob("os-check", platform.JobStatus{})
	h.fake.SetJob("packages", platform.JobStatus{State: "running", Started: time.Now().Unix()})
	if _, _, body = h.get(h.client, "/admin/updates"); !strings.Contains(body, `content="3;url=/admin/updates"`) {
		t.Error("Update now doesn't refresh")
	}
	h.fake.SetJob("packages", platform.JobStatus{})
	if _, _, body = h.get(h.client, "/admin/updates"); strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("refreshes with nothing running")
	}
}

// TestHealthCards: problems show on every admin page, with the page that
// helps; with none, nothing is shown.
func TestHealthCards(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	if _, _, body := h.get(h.client, "/admin/station"); strings.Contains(body, `class="health"`) {
		t.Error("cards shown with nothing wrong")
	}
	h.fake.SetHealth(platform.Problem{Level: "error", Title: "The gateway keeps restarting: it started 4 times in the last 10 minutes.",
		Detail: "Error creating MMDVM modem: modem returned NAK", Page: "logs"})
	h.srv.mu.Lock()
	h.srv.healthAt = time.Time{} // expire the cache
	h.srv.mu.Unlock()
	for _, p := range []string{"/admin/station", "/admin/system"} {
		_, _, body := h.get(h.client, p)
		if !strings.Contains(body, "keeps restarting") || !strings.Contains(body, "modem returned NAK") || !strings.Contains(body, `<a href="/admin/logs">Logs →</a>`) {
			t.Errorf("%s: no health card:\n%s", p, body)
		}
	}
}

// TestAdminUpdates: one Check for updates finds the system version and the
// package updates; each installs from its own button.
func TestAdminUpdates(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	page := func() string { _, _, b := h.get(h.client, "/admin/updates"); return b }
	act := func(action string) {
		t.Helper()
		code, loc, _ := h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {action}})
		h.expectRedirect(code, loc, "/admin/updates#os")
	}

	body := page()
	if !strings.Contains(body, "2026.11.23.1") || !strings.Contains(body, "Go back to 2026.11.20.1") || !strings.Contains(body, ">Check for updates<") {
		t.Fatalf("the Updates card:\n%s", body)
	}
	if strings.Contains(body, "M17 software</h2>") {
		t.Error("the separate M17 software card is still there")
	}
	act("check")
	body = page()
	for _, want := range []string{"System version 2026.11.25.1 is available", "install it first", ">Install 2026.11.25.1<", "M17 software updates:</strong> m17-gateway 0.9.10-r0", ">Update packages<"} {
		if !strings.Contains(body, want) {
			t.Errorf("after the check, no %q", want)
		}
	}
	act("install")
	body = page()
	if !strings.Contains(body, "System version 2026.11.25.1 is installed.") || !strings.Contains(body, ">Restart now<") || strings.Contains(body, ">Install 2026.11.25.1<") {
		t.Errorf("after installing:\n%s", body)
	}
	act("packages")
	if body = page(); !strings.Contains(body, "Updated: m17-gateway 0.9.10-r0") || strings.Contains(body, "M17 software updates:") {
		t.Errorf("after updating packages:\n%s", body)
	}
	code, _, body := h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {"reboot"}})
	if code != 200 || !strings.Contains(body, "Restarting") {
		t.Errorf("restart: %d", code)
	}
}

// TestAdminUpdatesOlderHelper: on an image whose helper checks the OS only,
// Check for updates falls back to that, and Update packages stays offered.
func TestAdminUpdatesOlderHelper(t *testing.T) {
	h := newHarness(t)
	h.runWizard(h.client)
	h.fake.SetNoCheckJob(true)
	code, loc, _ := h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {"check"}})
	h.expectRedirect(code, loc, "/admin/updates#os")
	_, _, body := h.get(h.client, "/admin/updates")
	if !strings.Contains(body, "System version 2026.11.25.1 is available") || !strings.Contains(body, ">Update packages<") {
		t.Errorf("OS-only check:\n%s", body)
	}
	// An image too old for any of it says so
	h.fake.SetUnsupported(true)
	code, _, body = h.postFrom(h.client, "/admin/updates", "/admin/updates/os", url.Values{"action": {"packages"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "This system version can") {
		t.Errorf("old image: %d", code)
	}
}

// TestFrontPage: / is the wizard until the hotspot is set up, then the
// dashboard; rpi-dashboard's old addresses lead to their replacements.
func TestFrontPage(t *testing.T) {
	h := newHarness(t)
	code, loc, _ := h.get(h.client, "/")
	h.expectRedirect(code, loc, "/setup/")
	h.runWizard(h.client)
	code, loc, _ = h.get(h.client, "/")
	h.expectRedirect(code, loc, "/dashboard/")
	for from, to := range map[string]string{
		"/index.php": "/dashboard/", "/map.php": "/dashboard/map", "/messages.php": "/dashboard/messages",
		"/config_gateway.php": "/admin/expert", "/help.php": "/dashboard/help", "/config_dashboard.php": "/admin/system", "/login.php": "/setup/login",
	} {
		code, loc, _ := h.get(h.client, from)
		if code != http.StatusMovedPermanently || loc != to {
			t.Errorf("%s: %d %q, want 301 to %s", from, code, loc, to)
		}
	}
}
