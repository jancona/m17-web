// Package web is m17-web's HTTP interface: the setup wizard under
// /setup/, and the captive-portal answers that make phones open it.
package web

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jancona/m17-web/internal/activity"
	"github.com/jancona/m17-web/internal/auth"
	"github.com/jancona/m17-web/internal/firmware"
	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/settings"
	"github.com/jancona/m17-web/internal/wizard"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server serves the setup wizard.
type Server struct {
	plat     platform.Platform
	store    *wizard.Store
	sessions *auth.Sessions
	log      *slog.Logger
	pages    map[string]*template.Template
	mux      *http.ServeMux

	activity *activity.Log // nil: no activity page

	mu         sync.Mutex
	lastResult *appliedResult    // what the last Apply did, for /setup/done
	notices    map[string]notice // per session: what the last admin Save did
	detected   *detection        // the last HAT detection from the Radio page
}

// appliedResult is what applying the wizard's settings did.
type appliedResult struct {
	Result platform.Result
	Draft  settings.Draft
	at     time.Time
}

// New returns a Server using plat, keeping the wizard's progress in store.
func New(plat platform.Platform, store *wizard.Store, log *slog.Logger) (*Server, error) {
	s := &Server{
		plat:     plat,
		store:    store,
		sessions: auth.NewSessions(),
		log:      log,
		pages:    map[string]*template.Template{},
		mux:      http.NewServeMux(),
		notices:  map[string]notice{},
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// Sessions exposes the session store (tests make failed logins instant).
func (s *Server) Sessions() *auth.Sessions { return s.sessions }

func (s *Server) routes() {
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /setup/static/", http.StripPrefix("/setup/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /{$}", s.redirectToSetup)
	s.mux.HandleFunc("GET /setup", s.redirectToSetup)
	s.mux.HandleFunc("GET /setup/{$}", s.start)
	s.mux.HandleFunc("GET /setup/login", s.loginForm)
	s.mux.HandleFunc("POST /setup/login", s.login)
	s.mux.HandleFunc("POST /setup/logout", s.logout)
	s.mux.HandleFunc("GET /setup/done", s.done)
	s.mux.HandleFunc("GET /setup/activity", s.activityPage)
	s.mux.HandleFunc("GET /setup/api/activity", s.activityJSON)
	s.mux.HandleFunc("GET /setup/{step}", s.showStep)
	s.mux.HandleFunc("POST /setup/{step}", s.submitStep)
	s.adminRoutes()

	// Captive-portal checks. While the setup access point is up, nginx
	// sends every host name here; answering these with a redirect makes
	// phones and laptops open the wizard by themselves.
	for _, p := range []string{
		"/generate_204", "/gen_204", // Android, Chrome
		"/hotspot-detect.html", "/library/test/success.html", // Apple
		"/connecttest.txt", "/ncsi.txt", "/redirect", // Windows
		"/success.txt", "/canonical.html", // Firefox
	} {
		s.mux.HandleFunc("GET "+p, s.captive)
	}
}

// ServeHTTP adds security headers and a request log to every response.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if !strings.HasPrefix(r.URL.Path, "/setup/static/") {
		h.Set("Cache-Control", "no-store")
	}
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w, status: 200}
	s.mux.ServeHTTP(sw, r)
	s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) redirectToSetup(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/setup/", http.StatusFound)
}

func (s *Server) captive(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "http://"+r.Host+"/setup/", http.StatusFound)
}

// ── Access ────────────────────────────────────────────────────────────

// access decides whether the request may continue. Until an admin
// password exists, only the password step is open; after that, only a
// logged-in session gets in.
func (s *Server) access(w http.ResponseWriter, r *http.Request, sess *auth.Session, step string) bool {
	set, err := s.plat.AdminPasswordSet()
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	if !set {
		if step != "password" {
			http.Redirect(w, r, "/setup/password", http.StatusFound)
			return false
		}
		return true
	}
	if !sess.LoggedIn {
		http.Redirect(w, r, "/setup/login", http.StatusFound)
		return false
	}
	return true
}

// checkCSRF rejects a POST without the session's token.
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request, sess *auth.Session) bool {
	if !sess.CheckCSRF(r.PostFormValue("csrf")) {
		http.Error(w, "This form has expired. Go back, reload the page and try again.", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.Get(w, r)
	set, err := s.plat.AdminPasswordSet()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !set {
		http.Redirect(w, r, "/setup/password", http.StatusFound)
		return
	}
	if sess.LoggedIn {
		http.Redirect(w, r, s.home(r.Context()), http.StatusFound)
		return
	}
	s.render(w, r, http.StatusOK, "login", &page{Title: "Log in", CSRF: sess.CSRF})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.Get(w, r)
	if !s.checkCSRF(w, r, sess) {
		return
	}
	ok, err := s.plat.CheckAdminPassword(r.PostFormValue("password"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		s.sessions.Failed()
		p := &page{Title: "Log in", CSRF: sess.CSRF, Errors: map[string]string{"password": "That isn't the admin password."}}
		s.render(w, r, http.StatusUnauthorized, "login", p)
		return
	}
	s.sessions.LogIn(w, sess)
	http.Redirect(w, r, s.home(r.Context()), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.Get(w, r)
	if !s.checkCSRF(w, r, sess) {
		return
	}
	s.sessions.LogOut(w, sess)
	http.Redirect(w, r, "/setup/login", http.StatusSeeOther)
}

// ── The wizard ────────────────────────────────────────────────────────

// state loads the wizard's progress, filling a new draft from the
// hotspot's current settings.
func (s *Server) state(ctx context.Context) (wizard.State, error) {
	st, err := s.store.Load()
	if err != nil {
		return st, err
	}
	if st.Started && st.Draft.SavedSSIDs == nil {
		// A draft from before m17-web tracked the saved networks: fetch
		// them, so a saved network isn't rewritten without its password
		if cur, err := s.plat.Current(ctx); err == nil && len(cur.WiFiSSIDs) > 0 {
			st.Draft.SavedSSIDs = cur.WiFiSSIDs
			st.Draft.UpdateKeepWiFi()
			if err := s.store.Save(st); err != nil {
				return st, err
			}
		}
	}
	if !st.Started {
		cur, err := s.plat.Current(ctx)
		if err != nil {
			return st, err
		}
		st.Draft = settings.FromCurrent(cur)
		st.Started = true
		if err := s.store.Save(st); err != nil {
			return st, err
		}
	}
	return st, nil
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.Get(w, r)
	if !s.access(w, r, sess, "") {
		return
	}
	// A hotspot that is set up opens on the admin pages, unless setup is
	// being run again or was started and not finished
	if saved, err := s.store.Load(); err == nil && !saved.Rerun && len(saved.Done) == 0 && s.setupDone(r.Context()) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	st, err := s.state(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/setup/"+st.FirstOpen(), http.StatusFound)
}

func (s *Server) showStep(w http.ResponseWriter, r *http.Request) {
	step := r.PathValue("step")
	if wizard.Index(step) < 0 {
		http.NotFound(w, r)
		return
	}
	sess := s.sessions.Get(w, r)
	if !s.access(w, r, sess, step) {
		return
	}
	st, err := s.state(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.Reachable(step) {
		http.Redirect(w, r, "/setup/"+st.FirstOpen(), http.StatusFound)
		return
	}
	p, err := s.newPage(r.Context(), sess, &st, step)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if step == "review" {
		p.Warnings = s.allWarnings(r.Context(), st.Draft)
	}
	s.render(w, r, http.StatusOK, step, p)
}

// newPage builds the data a step's template needs.
func (s *Server) newPage(ctx context.Context, sess *auth.Session, st *wizard.State, step string) (*page, error) {
	set, err := s.plat.AdminPasswordSet()
	if err != nil {
		return nil, err
	}
	p := &page{
		Title:       wizard.Steps[wizard.Index(step)].Title,
		Step:        step,
		Steps:       wizard.Steps,
		State:       st,
		Draft:       st.Draft,
		CSRF:        sess.CSRF,
		LoggedIn:    sess.LoggedIn,
		PasswordSet: set,
		Prev:        wizard.Prev(step),
		Errors:      map[string]string{},
		Warnings:    map[string]string{},
	}
	if err := s.loadSections(ctx, p, stepSections[step]...); err != nil {
		return nil, err
	}
	switch step {
	case "hat":
		hat, err := s.plat.DetectHAT(ctx)
		if err != nil {
			s.log.Warn("HAT detection failed", "err", err)
		}
		p.HAT = hat
		p.HATChecked = true
		st.DetectedHAT = hat
		if err := s.store.Save(*st); err != nil {
			return nil, err
		}
	case "firmware":
		fw, err := s.firmwareInfo(ctx, st)
		if err != nil {
			return nil, err
		}
		p.Firmware = fw
		if fw.Status.Running() {
			p.Refresh = 3
		}
	}
	return p, nil
}

// strongest keeps each network name once, with its strongest signal,
// strongest first.
func strongest(nets []platform.Network) []platform.Network {
	best := map[string]platform.Network{}
	for _, n := range nets {
		if n.SSID == "" {
			continue // hidden network
		}
		if b, ok := best[n.SSID]; !ok || n.Signal > b.Signal {
			best[n.SSID] = n
		}
	}
	out := make([]platform.Network, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b platform.Network) int {
		return cmp.Or(cmp.Compare(b.Signal, a.Signal), cmp.Compare(a.SSID, b.SSID))
	})
	return out
}

func (s *Server) submitStep(w http.ResponseWriter, r *http.Request) {
	step := r.PathValue("step")
	if wizard.Index(step) < 0 {
		http.NotFound(w, r)
		return
	}
	sess := s.sessions.Get(w, r)
	if !s.checkCSRF(w, r, sess) || !s.access(w, r, sess, step) {
		return
	}
	ctx := r.Context()
	st, err := s.state(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.Reachable(step) {
		http.Redirect(w, r, "/setup/"+st.FirstOpen(), http.StatusSeeOther)
		return
	}

	d := st.Draft
	f := r.PostForm
	var probs settings.Problems
	if ids, ok := stepSections[step]; ok {
		in := sectionInput{form: f, prev: st.Draft, detected: st.DetectedHAT, detectRan: true}
		if probs, err = s.parseSections(ctx, in, &d, ids...); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	switch step {
	case "password":
		pw := f.Get("password")
		set, err := s.plat.AdminPasswordSet()
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// Once a password exists, changing it here is optional
		if !set || pw != "" {
			probs = settings.CheckAdminPassword(pw, f.Get("confirm"))
			if probs.OK() {
				if err := s.plat.SetAdminPassword(ctx, pw); err != nil {
					probs.Errors["password"] = "The password couldn't be set: " + err.Error()
				} else if !sess.LoggedIn {
					s.sessions.LogIn(w, sess)
				}
			}
		}
	case "firmware":
		if s.submitFirmware(w, r, sess, &st) {
			return
		}
	case "review":
		s.apply(w, r, sess, st)
		return
	}

	if !probs.OK() {
		st.Draft = d
		p, err := s.newPage(ctx, sess, &st, step)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		p.Errors, p.Warnings = probs.Errors, probs.Warnings
		s.render(w, r, http.StatusUnprocessableEntity, step, p)
		return
	}
	st.Draft = d
	st.MarkDone(step)
	if err := s.store.Save(st); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/setup/"+wizard.Next(step), http.StatusSeeOther)
}

// allWarnings collects the warnings of every step, for the review page.
func (s *Server) allWarnings(ctx context.Context, d settings.Draft) map[string]string {
	w := map[string]string{}
	zones, _ := s.plat.TimeZones(ctx)
	for _, p := range []settings.Problems{d.CheckStation(zones), d.CheckRadio(), d.CheckSSH(d.UserPassword)} {
		for k, v := range p.Warnings {
			w[k] = v
		}
	}
	return w
}

// apply sends the draft to the platform and shows the result.
func (s *Server) apply(w http.ResponseWriter, r *http.Request, sess *auth.Session, st wizard.State) {
	res, err := s.plat.Apply(r.Context(), st.Draft.Settings())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("settings applied", "warnings", len(res.Warnings), "reboot", res.RebootNeeded, "network", res.NetworkChanged)
	if err := s.store.MarkSetupDone(); err != nil {
		s.log.Warn("recording that setup is done", "err", err)
	}
	// The draft may hold the WiFi password: don't keep it
	if err := s.store.Clear(); err != nil {
		s.log.Warn("clearing the wizard state", "err", err)
	}
	st.Draft.WiFiPassword = ""
	st.Draft.UserPassword = ""
	s.mu.Lock()
	s.lastResult = &appliedResult{Result: res, Draft: st.Draft, at: time.Now()}
	s.mu.Unlock()
	http.Redirect(w, r, "/setup/done", http.StatusSeeOther)
}

func (s *Server) done(w http.ResponseWriter, r *http.Request) {
	sess := s.sessions.Get(w, r)
	if !s.access(w, r, sess, "done") {
		return
	}
	s.mu.Lock()
	res := s.lastResult
	s.mu.Unlock()
	if res == nil {
		http.Redirect(w, r, "/setup/", http.StatusFound)
		return
	}
	p := &page{
		Title:    "Done",
		Step:     "done",
		CSRF:     sess.CSRF,
		LoggedIn: sess.LoggedIn,
		Draft:    res.Draft,
		Result:   &res.Result,
	}
	s.render(w, r, http.StatusOK, "done", p)
}

// ── Rendering ─────────────────────────────────────────────────────────

// page is the data every template gets.
type page struct {
	Title       string
	Step        string
	Steps       []wizard.Step
	State       *wizard.State
	Draft       settings.Draft
	CSRF        string
	LoggedIn    bool
	PasswordSet bool
	Prev        string
	Errors      map[string]string
	Warnings    map[string]string

	Networks   []platform.Network
	Countries  []settings.Country
	TimeZones  []string
	HAT        platform.HAT
	Firmware   *firmwareInfo
	Reflectors []platform.Reflector
	Result     *platform.Result
	// HATChecked is set once the HAT has been asked what it is (HAT holds
	// the answer)
	HATChecked bool

	// The admin pages
	Admin     bool
	AdminPage adminPage
	Nav       []navGroup
	Notice    *notice
	// ID is the section being rendered by the "section" template
	ID string
	// Panels is what the admin pages' panels show
	Panels panelData
	// GatewayModem is what the gateway reported about the HAT when it
	// started (the Radio page, when no detection has been done)
	GatewayModem *platform.SystemInfo
	// Message is the text of the message page; RefreshURL, if set, is
	// where Refresh goes
	Message    string
	RefreshURL string
	// Refresh, if set, reloads the page after this many seconds
	Refresh int

	Status  *activity.Status
	Entries []activity.Entry
}

func (s *Server) parseTemplates() error {
	funcs := template.FuncMap{
		"mhz": func(f float64) string {
			if f <= 0 || f != f {
				return ""
			}
			return strconv.FormatFloat(f, 'f', -1, 64)
		},
		"hours": func() []int {
			h := make([]int, 24)
			for i := range h {
				h[i] = i
			}
			return h
		},
		"modules": func() []string {
			m := make([]string, 26)
			for i := range m {
				m[i] = string(rune('A' + i))
			}
			return m
		},
		"familyTitle": firmware.FamilyTitle,
		"withSection": withSection,
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"updateAvailable": updateAvailable,
		"updateResult":    updateResult,
		"connectedNote": func(w platform.WiFiStatus, ssid string) string {
			if w.Connected() && w.SSID == ssid {
				return " The hotspot is connected to it now, so it will disconnect."
			}
			return ""
		},
		"uptime":    formatUptime,
		"bytes":     formatBytes,
		"logTitle":  func(name string) string { return logTitles[name] },
		"logHelp":   func(name string) string { return logHelps[name] },
		"stepIndex": func(id string) int { return wizard.Index(id) + 1 },
		"done":      func(st *wizard.State, id string) bool { return st != nil && st.IsDone(id) },
		"signalBars": func(dbm int) string {
			switch {
			case dbm >= -55:
				return "▂▄▆█"
			case dbm >= -67:
				return "▂▄▆"
			case dbm >= -78:
				return "▂▄"
			default:
				return "▂"
			}
		},
		"clock": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			t = t.Local()
			if y, m, d := time.Now().Date(); t.Year() == y && t.Month() == m && t.Day() == d {
				return t.Format("15:04:05")
			}
			return t.Format("Jan 2 15:04")
		},
		"seconds": func(f *float64) string {
			if f == nil {
				return ""
			}
			d := time.Duration(*f * float64(time.Second)).Round(time.Second)
			if d < time.Minute {
				return fmt.Sprintf("%.1f s", *f)
			}
			return d.String()
		},
		"pct": func(f *float64) string {
			if f == nil {
				return ""
			}
			return fmt.Sprintf("%.1f%%", *f)
		},
		"band": func(mhz int) string {
			if mhz >= 5000 {
				return "5 GHz"
			}
			return "2.4 GHz"
		},
	}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, n := range names {
		name := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if name == "layout" || name == "sections" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/sections.html", n)
		if err != nil {
			return fmt.Errorf("template %s: %w", name, err)
		}
		s.pages[name] = t
	}
	return nil
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p *page) {
	t, ok := s.pages[name]
	if !ok {
		s.fail(w, r, fmt.Errorf("no template %q", name))
		return
	}
	var b strings.Builder
	if err := t.Execute(&b, p); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong: "+err.Error(), http.StatusInternalServerError)
}
