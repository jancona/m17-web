package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jancona/m17-web/internal/auth"
	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/settings"
)

// panelData is what the admin pages' panels show.
type panelData struct {
	Saved    []platform.SavedNetwork
	WiFi     platform.WiFiStatus
	Keys     []platform.SSHKey
	OS       platform.OSStatus
	Check    platform.JobStatus // the last update check
	Install  platform.JobStatus // the last update install
	Sys      platform.SystemInfo
	LogNames []string
	LogName  string
	LogLines []string
	// Unsupported lists the panels the system is too old for
	Unsupported map[string]bool
	// NewSSID is the network name typed in the add form (kept on errors)
	NewSSID string
}

// loadPanel fetches what panel id shows. q is the page's query (the Logs
// page's choice of log).
func (s *Server) loadPanel(ctx context.Context, p *page, id string, q url.Values) error {
	note := func(err error) error {
		if errors.Is(err, platform.ErrUnsupported) {
			p.Panels.Unsupported[id] = true
			return nil
		}
		return err
	}
	var err error
	switch id {
	case "wifistatus":
		p.Panels.WiFi, err = s.plat.WiFiStatus(ctx)
		return note(err)
	case "wifinets":
		if err := s.loadSections(ctx, p, "wifi"); err != nil {
			return err
		}
		p.Panels.Saved, err = s.plat.SavedWiFi(ctx)
		if err := note(err); err != nil {
			return err
		}
		if !p.Panels.Unsupported[id] {
			p.Panels.WiFi, err = s.plat.WiFiStatus(ctx)
			return note(err)
		}
	case "sshkeys":
		p.Panels.Keys, err = s.plat.SSHKeys(ctx)
		return note(err)
	case "os":
		p.Panels.OS, err = s.plat.OSStatus(ctx)
		if err := note(err); err != nil || p.Panels.Unsupported[id] {
			return err
		}
		if p.Panels.Check, err = s.plat.JobStatus(ctx, "os-check"); err != nil {
			return err
		}
		if p.Panels.Install, err = s.plat.JobStatus(ctx, "os-install"); err != nil {
			return err
		}
		if p.Panels.Check.Running() || p.Panels.Install.Running() {
			p.Refresh = 3
		}
	case "sysinfo":
		p.Panels.Sys, err = s.plat.SystemInfo(ctx)
		return err
	case "logs":
		p.Panels.LogNames = platform.LogNames
		p.Panels.LogName = q.Get("log")
		if !slices.Contains(platform.LogNames, p.Panels.LogName) {
			p.Panels.LogName = platform.LogNames[0]
		}
		p.Panels.LogLines, err = s.plat.Log(ctx, p.Panels.LogName)
		return note(err)
	}
	return nil
}

// updateAvailable is the version an update check found ("" if none).
func updateAvailable(j platform.JobStatus) string {
	if j.State != "ok" || j.Exit != 0 {
		return ""
	}
	// m17os-upgrade --check: "Available:  2026.11.25.1 (2026-11-25)"
	for _, l := range j.Log {
		if v, ok := strings.CutPrefix(l, "Available:"); ok {
			if f := strings.Fields(v); len(f) > 0 {
				return f[0]
			}
		}
	}
	return "a new version"
}

// submitPanel handles a panel's form. It returns the problems to show on
// the page again, or answers the request itself (done).
func (s *Server) submitPanel(w http.ResponseWriter, r *http.Request, sess *auth.Session, id string, saved func(string, *platform.Result)) (probs settings.Problems, done bool) {
	ctx := r.Context()
	f := r.PostForm
	probs = settings.Problems{Errors: map[string]string{}, Warnings: map[string]string{}}
	fail := func(field string, err error) (settings.Problems, bool) {
		if errors.Is(err, platform.ErrUnsupported) {
			probs.Errors[field] = err.Error()
		} else {
			probs.Errors[field] = "That didn't work: " + err.Error()
		}
		return probs, false
	}
	reconnect := &platform.Result{NetworkChanged: true}

	switch id + "/" + f.Get("action") {
	case "wifinets/add":
		ssid := f.Get("wifi_ssid")
		if ssid == "" || ssid == "-other-" {
			ssid = strings.TrimSpace(f.Get("wifi_ssid_other"))
		}
		d := settings.Draft{UseWiFi: true, WiFiSSID: ssid, WiFiPassword: f.Get("wifi_password"), WiFiCountry: "US"}
		probs = d.CheckNetwork([]string{"US"})
		if probs.OK() && strings.ContainsAny(ssid, "\"\\") {
			probs.Errors["wifi_ssid"] = "This hotspot can't save a network name with a quote or backslash in it."
		}
		if !probs.OK() {
			return probs, false
		}
		if err := s.plat.AddWiFi(ctx, ssid, d.WiFiPassword); err != nil {
			return fail("wifi_ssid", err)
		}
		s.log.Info("WiFi network added", "ssid", ssid)
		saved("Added "+ssid+". The hotspot is reconnecting.", reconnect)
		return probs, true

	case "wifinets/remove":
		ssid := f.Get("ssid")
		if err := s.plat.RemoveWiFi(ctx, ssid); err != nil {
			return fail("saved", err)
		}
		s.log.Info("WiFi network removed", "ssid", ssid)
		saved("Removed "+ssid+".", reconnect)
		return probs, true

	case "sshkeys/remove":
		if err := s.plat.RemoveSSHKey(ctx, f.Get("key")); err != nil {
			return fail("keys", err)
		}
		s.log.Info("SSH key removed")
		saved("Key removed.", nil)
		return probs, true

	case "os/check", "os/install":
		job := "os-" + f.Get("action")
		if err := s.plat.StartJob(ctx, job); err != nil {
			return fail("os", err)
		}
		s.log.Info("job started", "job", job)
		http.Redirect(w, r, "/admin/updates#os", http.StatusSeeOther)
		return probs, true

	case "os/rollback":
		if err := s.plat.Rollback(ctx); err != nil {
			return fail("os", err)
		}
		s.log.Info("rolling back")
		s.restarting(w, r, sess, "Going back to the previous version")
		return probs, true

	case "os/reboot", "sysinfo/reboot":
		if err := s.plat.Reboot(ctx); err != nil {
			return fail(id, err)
		}
		s.log.Info("rebooting")
		s.restarting(w, r, sess, "Restarting")
		return probs, true

	case "sysinfo/shutdown":
		if err := s.plat.Shutdown(ctx); err != nil {
			return fail(id, err)
		}
		s.log.Info("shutting down")
		s.render(w, r, http.StatusOK, "message", &page{
			Title: "Shutting down", CSRF: sess.CSRF,
			Message: "The hotspot is shutting down. Wait until its green light stops flashing before removing the power.",
		})
		return probs, true
	}
	http.Error(w, "Unknown action", http.StatusBadRequest)
	return probs, true
}

// restarting shows that the hotspot is restarting; the page reloads the
// admin pages until they answer again.
func (s *Server) restarting(w http.ResponseWriter, r *http.Request, sess *auth.Session, what string) {
	s.render(w, r, http.StatusOK, "message", &page{
		Title: what, CSRF: sess.CSRF, Refresh: 20, RefreshURL: "/admin/system",
		Message: what + ": this takes about a minute. This page reloads by itself; if it doesn't, open it again.",
	})
}

// updateResult says what an automatic update run's RESULT means.
func updateResult(r string) string {
	switch r {
	case "current":
		return "up to date"
	case "available":
		return "an update is available"
	case "installed":
		return "a new system version was installed"
	case "updated":
		return "M17 packages were updated"
	case "busy":
		return "postponed: the gateway was busy"
	case "failed":
		return "failed"
	case "skipped":
		return "skipped"
	}
	return r
}

// formatUptime is a duration as days, hours and minutes.
func formatUptime(d time.Duration) string {
	d = d.Truncate(time.Minute)
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d d %d h", days, h)
	case h > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	}
	return fmt.Sprintf("%d min", m)
}

// formatBytes is a size in MB or GB.
func formatBytes(n uint64) string {
	if n >= 10<<30 {
		return fmt.Sprintf("%.0f GB", float64(n)/(1<<30))
	}
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}

var logTitles = map[string]string{
	"events":  "Events",
	"gateway": "Gateway",
	"system":  "System",
	"web":     "Web pages",
}

var logHelps = map[string]string{
	"events":  "Updates, rollbacks, health checks, setup changes: the hotspot's own diary, kept across restarts.",
	"gateway": "m17-gateway's log since it last started.",
	"system":  "Everything the system logs, since the last restart.",
	"web":     "These pages' own log since they last started.",
}
