package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jancona/m17-web/internal/activity"
	"github.com/jancona/m17-web/internal/platform"
)

// dashData is what the dashboard shows.
type dashData struct {
	Station platform.Current
	// Live is false when m17-web runs without an MQTT broker
	Live    bool
	Status  activity.Status
	OnAir   []activity.Entry // being heard now
	Entries []activity.Entry // last heard, newest first
}

// dashEntries is how many last-heard entries the dashboard shows.
const dashEntries = 50

// sseKeepalive is how often an idle event stream sends a comment, so
// proxies and browsers keep it open.
const sseKeepalive = 25 * time.Second

func (s *Server) dashboardRoutes() {
	s.mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})
	s.mux.HandleFunc("GET /dashboard/{$}", s.dashboard)
	s.mux.HandleFunc("GET /dashboard/live", s.dashboardLive)
	s.mux.HandleFunc("GET /dashboard/events", s.dashboardEvents)
	// The activity page it replaces
	s.mux.HandleFunc("GET /setup/activity", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
	})
}

// dashPage builds the dashboard's page data. Like the old dashboard it
// needs no login: it shows only what's on the air.
func (s *Server) dashPage(w http.ResponseWriter, r *http.Request) (*page, error) {
	sess := s.sessions.Get(w, r)
	cur, err := s.plat.Current(r.Context())
	if err != nil {
		return nil, err
	}
	d := dashData{Station: cur, Live: s.activity != nil}
	if s.activity != nil {
		var entries []activity.Entry
		d.Status, entries = s.activity.Snapshot()
		for _, e := range entries {
			if e.Active {
				d.OnAir = append(d.OnAir, e)
			}
		}
		d.Entries = entries[:min(len(entries), dashEntries)]
	}
	return &page{Title: "Dashboard", Public: true, Dash: &d, LoggedIn: sess.LoggedIn, CSRF: sess.CSRF, NoScriptRefresh: 15}, nil
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	p, err := s.dashPage(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", p)
}

// dashboardLive is the live part of the dashboard alone, which the page's
// script fetches when the event stream says something changed.
func (s *Server) dashboardLive(w http.ResponseWriter, r *http.Request) {
	p, err := s.dashPage(w, r)
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
