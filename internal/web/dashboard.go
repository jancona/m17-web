package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jancona/m17-web/internal/activity"
	"github.com/jancona/m17-web/internal/platform"
)

// dashData is what the dashboard shows.
type dashData struct {
	// Tab is the dashboard page: "status", "messages" or "map"
	Tab  string
	Tabs []dashTab
	// LivePath is where the page's script fetches its live part
	LivePath string
	Station  platform.Current
	// Live is false when m17-web runs without an MQTT broker
	Live    bool
	Status  activity.Status
	OnAir   []activity.Entry // being heard now
	Entries []activity.Entry // last heard, newest first
	// Messages are the text messages, newest first (messages tab)
	Messages []activity.Entry
	// Positions are the stations' last positions, newest first (map tab)
	Positions []activity.StationPosition
	// Tiles is the map's tile URL template (map tab)
	Tiles string
}

// dashTab is one of the dashboard's pages.
type dashTab struct{ ID, Path, Title string }

// LivePath is where the page's script fetches its live part.
func (t dashTab) LivePath() string {
	return strings.TrimSuffix(t.Path, "/") + "/live"
}

// dashTabs are the dashboard's pages.
var dashTabs = []dashTab{
	{"status", "/dashboard/", "Dashboard"},
	{"messages", "/dashboard/messages", "Messages"},
	{"map", "/dashboard/map", "Map"},
}

// mapMaxAge: the map shows stations heard from in this time.
const mapMaxAge = 30 * 24 * time.Hour

// mapTiles is where the map's tiles come from, the one thing the dashboard
// loads from outside the hotspot.
const mapTiles = "https://tile.openstreetmap.org"

// dashEntries is how many last-heard entries the dashboard shows.
const dashEntries = 50

// sseKeepalive is how often an idle event stream sends a comment, so
// proxies and browsers keep it open.
const sseKeepalive = 25 * time.Second

func (s *Server) dashboardRoutes() {
	s.mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})
	s.mux.HandleFunc("GET /dashboard/{$}", s.dashboard("status"))
	s.mux.HandleFunc("GET /dashboard/live", s.dashboardLive("status"))
	s.mux.HandleFunc("GET /dashboard/messages", s.dashboard("messages"))
	s.mux.HandleFunc("GET /dashboard/messages/live", s.dashboardLive("messages"))
	s.mux.HandleFunc("GET /dashboard/map", s.dashboard("map"))
	s.mux.HandleFunc("GET /dashboard/map/live", s.dashboardLive("map"))
	s.mux.HandleFunc("GET /dashboard/positions.json", s.positionsJSON)
	s.mux.HandleFunc("GET /dashboard/events", s.dashboardEvents)
	// The activity page it replaces
	s.mux.HandleFunc("GET /setup/activity", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
	})
}

// dashPage builds the dashboard's page data. Like the old dashboard it
// needs no login: it shows only what's on the air.
func (s *Server) dashPage(w http.ResponseWriter, r *http.Request, tab string) (*page, error) {
	sess := s.sessions.Get(w, r)
	cur, err := s.plat.Current(r.Context())
	if err != nil {
		return nil, err
	}
	d := dashData{Tab: tab, Tabs: dashTabs, Station: cur, Live: s.activity != nil}
	title, live := "Dashboard", "/dashboard/live"
	for _, t := range dashTabs {
		if t.ID == tab {
			title, live = t.Title, t.LivePath()
		}
	}
	d.LivePath = live
	if tab == "map" {
		d.Tiles = mapTiles + "/{z}/{x}/{y}.png"
	}
	switch {
	case s.activity == nil:
	case tab == "messages":
		d.Status, _ = s.activity.Snapshot()
		d.Messages = s.activity.Messages()
	case tab == "map":
		d.Status, _ = s.activity.Snapshot()
		d.Positions = s.activity.Positions(time.Now().Add(-mapMaxAge))
	default:
		var entries []activity.Entry
		d.Status, entries = s.activity.Snapshot()
		for _, e := range entries {
			if e.Active {
				d.OnAir = append(d.OnAir, e)
			}
		}
		d.Entries = entries[:min(len(entries), dashEntries)]
	}
	return &page{Title: title, Public: true, Dash: &d, LoggedIn: sess.LoggedIn, CSRF: sess.CSRF, NoScriptRefresh: 15}, nil
}

func (s *Server) dashboard(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.dashPage(w, r, tab)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tab == "map" {
			// The map's tiles
			w.Header().Set("Content-Security-Policy", strings.Replace(csp, "img-src 'self' data:", "img-src 'self' data: "+mapTiles, 1))
			p.Styles = []string{"/setup/static/leaflet/leaflet.css"}
		}
		s.render(w, r, http.StatusOK, "dashboard", p)
	}
}

// positionsJSON is the stations' last positions, for the map's script.
func (s *Server) positionsJSON(w http.ResponseWriter, r *http.Request) {
	ps := []activity.StationPosition{}
	if s.activity != nil {
		ps = append(ps, s.activity.Positions(time.Now().Add(-mapMaxAge))...)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ps)
}

// dashboardLive is the live part of the dashboard alone, which the page's
// script fetches when the event stream says something changed.
func (s *Server) dashboardLive(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.dashPage(w, r, tab)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		var b strings.Builder
		if err := s.pages["dashboard"].ExecuteTemplate(&b, "dash-live", p); err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
}

// dashboardEvents is a Server-Sent Events stream: an "update" event each
// time the activity log changes (the page then fetches dashboardLive), and
// a comment every sseKeepalive.
func (s *Server) dashboardEvents(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		http.NotFound(w, r)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming isn't supported", http.StatusInternalServerError)
		return
	}
	// The server's write timeout is for ordinary pages: this stream stays
	// open until the browser goes
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		s.log.Warn("dashboard events: can't clear the write deadline", "err", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no") // nginx: pass each event on at once
	w.WriteHeader(http.StatusOK)
	// Tell the browser to retry after 5 s if the stream breaks
	fmt.Fprint(w, "retry: 5000\n\n")
	fl.Flush()
	keep := time.NewTicker(sseKeepalive)
	defer keep.Stop()
	changed := s.activity.Changed()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
		case <-changed:
			// The next change's channel now, so none is missed while this
			// one is sent. A transmission is a burst of messages: wait a
			// moment and send one update for them.
			changed = s.activity.Changed()
			select {
			case <-time.After(250 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, "event: update\ndata: {}\n\n")
		}
		fl.Flush()
	}
}
