package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jancona/m17-web/internal/auth"
	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/settings"
	"github.com/jancona/m17-web/internal/wizard"
)

// adminPage is a page of the admin pages: a few sections, each with its
// own form and Save.
type adminPage struct {
	ID       string
	Title    string
	Group    string // the sidebar heading it's under ("" for none)
	Intro    string
	Sections []adminSection
}

// adminSection is a section as an admin page shows it.
type adminSection struct {
	ID    string // a key of sections, or adminpw or rerun
	Title string
	Help  string
	// Button is the submit button's label ("Save" if empty)
	Button string
	// Panel sections draw their own forms (lists with Remove buttons,
	// actions); the page doesn't wrap them in one with a Save button
	Panel bool
}

var adminPages = []adminPage{
	{ID: "station", Title: "Station", Sections: []adminSection{
		{ID: "callsign", Title: "Callsign"},
		{ID: "reflector", Title: "Reflector", Help: "The reflector the gateway links to when it starts."},
	}},
	{ID: "radio", Title: "Radio", Group: "Setup", Sections: []adminSection{
		{ID: "hat", Title: "Radio HAT"},
		{ID: "frequency", Title: "Frequency and power"},
	}},
	{ID: "firmware", Title: "Firmware", Group: "Setup", Sections: []adminSection{
		{ID: "firmware", Title: "HAT firmware", Panel: true},
	}},
	{ID: "network", Title: "Network", Group: "Setup", Sections: []adminSection{
		{ID: "wifistatus", Title: "Connection", Panel: true},
		{ID: "wifinets", Title: "WiFi networks", Panel: true, Help: "The hotspot joins the first of these it finds, newest first."},
		{ID: "wificountry", Title: "WiFi country"},
		{ID: "hostname", Title: "Hotspot name"},
	}},
	{ID: "updates", Title: "Updates", Group: "Administration", Sections: []adminSection{
		{ID: "os", Title: "System version", Panel: true},
		{ID: "updates", Title: "Automatic updates"},
	}},
	{ID: "security", Title: "Security", Group: "Administration", Sections: []adminSection{
		{ID: "adminpw", Title: "Admin password", Button: "Change password"},
		{ID: "sshkeys", Title: "SSH keys", Panel: true, Help: "Computers holding one of these keys can log in as m17."},
		{ID: "ssh", Title: "SSH", Help: "Logging in to the hotspot from a computer, as the user m17."},
	}},
	{ID: "system", Title: "System", Group: "Administration", Sections: []adminSection{
		{ID: "sysinfo", Title: "This hotspot", Panel: true},
		{ID: "timezone", Title: "Time zone"},
		{ID: "rerun", Title: "Run setup again", Button: "Run setup again"},
	}},
	{ID: "logs", Title: "Logs", Group: "Administration", Sections: []adminSection{
		{ID: "logs", Title: "Logs", Panel: true},
	}},
}

// navGroup is a heading of the admin sidebar and its pages.
type navGroup struct {
	Title string
	Pages []adminPage
}

// adminNav groups the admin pages for the sidebar.
func adminNav() []navGroup {
	var g []navGroup
	for _, p := range adminPages {
		if len(g) == 0 || g[len(g)-1].Title != p.Group {
			g = append(g, navGroup{Title: p.Group})
		}
		g[len(g)-1].Pages = append(g[len(g)-1].Pages, p)
	}
	return g
}

func findAdminPage(id string) (adminPage, bool) {
	i := slices.IndexFunc(adminPages, func(p adminPage) bool { return p.ID == id })
	if i < 0 {
		return adminPage{}, false
	}
	return adminPages[i], true
}

func (p adminPage) section(id string) (adminSection, bool) {
	i := slices.IndexFunc(p.Sections, func(s adminSection) bool { return s.ID == id })
	if i < 0 {
		return adminSection{}, false
	}
	return p.Sections[i], true
}

// notice is what saving a section did, shown once on the page after.
type notice struct {
	Section string
	Message string
	Result  *platform.Result
}

// detection is the last HAT detection from the Radio page.
type detection struct {
	HAT platform.HAT
	At  time.Time
}

// detectionShown is how long a detection's result stays on the Radio page.
const detectionShown = 10 * time.Minute

func (s *Server) adminRoutes() {
	s.mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	s.mux.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/"+adminPages[0].ID, http.StatusFound)
	})
	s.mux.HandleFunc("GET /admin/{page}", s.showAdmin)
	s.mux.HandleFunc("POST /admin/{page}/{section}", s.submitAdmin)
}

// setupDone reports whether the hotspot has been set up: setup was applied
// here, or it has a callsign (set up before the marker, or by hand).
func (s *Server) setupDone(ctx context.Context) bool {
	if s.store.SetupDone() {
		return true
	}
	cur, err := s.plat.Current(ctx)
	return err == nil && cur.Callsign != ""
}

// home is where a logged-in user goes: the admin pages once the hotspot
// is set up, the wizard before.
func (s *Server) home(ctx context.Context) string {
	if s.setupDone(ctx) {
		return "/admin/"
	}
	return "/setup/"
}

// adminAccess lets a logged-in user in; others go to log in (or, with no
// password yet, to setup).
func (s *Server) adminAccess(w http.ResponseWriter, r *http.Request, sess *auth.Session) bool {
	return s.access(w, r, sess, "")
}

// newAdminPage builds the page data for admin page ap from draft d.
func (s *Server) newAdminPage(ctx context.Context, sess *auth.Session, ap adminPage, d settings.Draft, q url.Values) (*page, error) {
	set, err := s.plat.AdminPasswordSet()
	if err != nil {
		return nil, err
	}
	p := &page{
		Title:       ap.Title,
		Admin:       true,
		AdminPage:   ap,
		Nav:         adminNav(),
		Draft:       d,
		CSRF:        sess.CSRF,
		LoggedIn:    sess.LoggedIn,
		PasswordSet: set,
		Errors:      map[string]string{},
		Warnings:    map[string]string{},
	}
	p.Panels.Unsupported = map[string]bool{}
	for _, sec := range ap.Sections {
		var err error
		if sec.Panel {
			err = s.loadPanel(ctx, p, sec.ID, q)
		} else {
			err = s.loadSections(ctx, p, sec.ID)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, ok := ap.section("hat"); ok {
		s.mu.Lock()
		det := s.detected
		s.mu.Unlock()
		if det != nil && time.Since(det.At) < detectionShown {
			p.HAT, p.HATChecked = det.HAT, true
		} else if si, err := s.plat.SystemInfo(ctx); err == nil && si.ModemIdent != "" {
			// What the gateway found when it started: no probe needed
			p.GatewayModem = &si
		}
	}
	return p, nil
}

// currentDraft is a draft of the hotspot's settings as they are now.
func (s *Server) currentDraft(ctx context.Context) (settings.Draft, error) {
	cur, err := s.plat.Current(ctx)
	if err != nil {
		return settings.Draft{}, err
	}
	return settings.FromCurrent(cur), nil
}

func (s *Server) showAdmin(w http.ResponseWriter, r *http.Request) {
	ap, ok := findAdminPage(r.PathValue("page"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	sess := s.sessions.Get(w, r)
	if !s.adminAccess(w, r, sess) {
		return
	}
	d, err := s.currentDraft(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.newAdminPage(r.Context(), sess, ap, d, r.URL.Query())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.mu.Lock()
	if n, ok := s.notices[sess.ID]; ok {
		p.Notice = &n
		delete(s.notices, sess.ID)
	}
	s.mu.Unlock()
	s.render(w, r, http.StatusOK, "admin", p)
}

func (s *Server) submitAdmin(w http.ResponseWriter, r *http.Request) {
	ap, ok := findAdminPage(r.PathValue("page"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("section")
	sec, ok := ap.section(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sess := s.sessions.Get(w, r)
	if !s.checkCSRF(w, r, sess) || !s.adminAccess(w, r, sess) {
		return
	}
	ctx := r.Context()
	f := r.PostForm
	back := "/admin/" + ap.ID + "#" + id
	saved := func(msg string, res *platform.Result) {
		s.mu.Lock()
		s.notices[sess.ID] = notice{Section: id, Message: msg, Result: res}
		s.mu.Unlock()
		http.Redirect(w, r, back, http.StatusSeeOther)
	}

	d, err := s.currentDraft(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var probs settings.Problems
	if sec.Panel {
		var done bool
		if probs, done = s.submitPanel(w, r, sess, id, saved); done {
			return
		}
	}
	switch {
	case sec.Panel:
		// problems from the panel: shown below
	case id == "rerun":
		// Start the wizard over from the current settings, every step open
		if err := s.store.Save(wizard.State{Rerun: true}); err != nil {
			s.fail(w, r, err)
			return
		}
		http.Redirect(w, r, "/setup/", http.StatusSeeOther)
		return

	case id == "adminpw":
		okPw, err := s.plat.CheckAdminPassword(f.Get("current"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !okPw {
			s.sessions.Failed()
			probs = settings.Problems{Errors: map[string]string{"current": "That isn't the current admin password."}}
			break
		}
		probs = settings.CheckAdminPassword(f.Get("password"), f.Get("confirm"))
		if probs.OK() {
			if err := s.plat.SetAdminPassword(ctx, f.Get("password")); err != nil {
				probs.Errors["password"] = "The password couldn't be set: " + err.Error()
				break
			}
			saved("Password changed.", nil)
			return
		}

	case id == "hat" && f.Get("action") == "detect":
		hat, err := s.plat.DetectHAT(ctx)
		if err != nil {
			s.log.Warn("HAT detection failed", "err", err)
		}
		s.mu.Lock()
		s.detected = &detection{HAT: hat, At: time.Now()}
		s.mu.Unlock()
		http.Redirect(w, r, back, http.StatusSeeOther)
		return

	default:
		def, ok := sections[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		in := sectionInput{form: f, prev: d}
		s.mu.Lock()
		if s.detected != nil && time.Since(s.detected.At) < detectionShown {
			in.detected, in.detectRan = s.detected.HAT, true
		}
		s.mu.Unlock()
		probs, err = def.parse(s, ctx, in, &d)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if probs.OK() {
			res, err := s.plat.Apply(ctx, def.keys(d))
			if err != nil {
				s.fail(w, r, err)
				return
			}
			s.log.Info("settings applied", "section", id, "warnings", len(res.Warnings), "reboot", res.RebootNeeded, "network", res.NetworkChanged)
			msg := "Saved."
			if len(res.Warnings) > 0 {
				msg = "Saved, with problems:"
			}
			saved(msg, &res)
			return
		}
	}

	// Errors: show the page again with what was typed, for this section
	p, err := s.newAdminPage(ctx, sess, ap, d, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if id == "wifinets" {
		p.Panels.NewSSID = f.Get("wifi_ssid_other")
	}
	p.Errors, p.Warnings = probs.Errors, probs.Warnings
	if p.Warnings == nil {
		p.Warnings = map[string]string{}
	}
	p.Notice = &notice{Section: sec.ID, Message: "Not saved: check the fields marked below."}
	s.render(w, r, http.StatusUnprocessableEntity, "admin", p)
}

// withSection is page p rendering section id (for the "section" template).
func withSection(p *page, id string) *page {
	c := *p
	c.ID = id
	return &c
}
