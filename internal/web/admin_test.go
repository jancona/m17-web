package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

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
			if !strings.Contains(body, `action="/admin/`+p.ID+`/`+s.ID+`"`) {
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
