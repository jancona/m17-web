package web

import (
	"encoding/json"
	"net/http"

	"github.com/jancona/m17-web/internal/activity"
)

// SetActivity turns on the activity page and API, showing log.
func (s *Server) SetActivity(log *activity.Log) { s.activity = log }

// activityPage shows the reflector link and recent activity. Like the
// dashboard, it needs no login: it shows only what's on the air.
func (s *Server) activityPage(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		// Started without a broker (-mqtt ""): say so rather than 404
		s.render(w, r, http.StatusOK, "activity", &page{Title: "Activity"})
		return
	}
	st, entries := s.activity.Snapshot()
	p := &page{Title: "Activity", Refresh: 10, Status: &st, Entries: entries}
	s.render(w, r, http.StatusOK, "activity", p)
}

// activityJSON is the same, for the dashboard and other pages:
//
//	{"status": {...}, "entries": [...newest first]}
func (s *Server) activityJSON(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		http.NotFound(w, r)
		return
	}
	st, entries := s.activity.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Status  activity.Status  `json:"status"`
		Entries []activity.Entry `json:"entries"`
	}{st, entries})
}
