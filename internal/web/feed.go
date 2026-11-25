package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jancona/m17-web/internal/activity"
)

// feedPrefs are a viewer's choices for the dashboard's list: which
// traffic, the latest per station only, kerchunks hidden. They come from
// the query (the filter links and the form) and are kept in a cookie, so
// each browser keeps its own.
type feedPrefs struct {
	Show          string // "all", "rf", "net" or "sms"
	Latest        bool   // only each station's latest per mode
	HideKerchunks bool
}

// kerchunk: a voice transmission shorter than this is a kerchunk (a
// key-up, often to check the hotspot is there)
const kerchunk = 1.5 // seconds

const prefsCookie = "m17dash"

var defaultPrefs = feedPrefs{Show: "all", HideKerchunks: true}

// readFeedPrefs reads the prefs from the query, saving them in the
// cookie, or else from the cookie.
func readFeedPrefs(w http.ResponseWriter, r *http.Request) feedPrefs {
	q := r.URL.Query()
	fromQuery := q.Get("f") != ""
	if !fromQuery {
		c, err := r.Cookie(prefsCookie)
		if err != nil {
			return defaultPrefs
		}
		if q, err = url.ParseQuery(c.Value); err != nil {
			return defaultPrefs
		}
	}
	p := feedPrefs{Show: q.Get("show"), Latest: q.Get("latest") == "1", HideKerchunks: q.Get("hide") == "1"}
	switch p.Show {
	case "all", "rf", "net", "sms":
	default:
		p.Show = "all"
	}
	if fromQuery {
		http.SetCookie(w, &http.Cookie{Name: prefsCookie, Value: p.Query(), Path: "/dashboard/",
			MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, HttpOnly: true})
	}
	return p
}

// Query is the prefs as a query string (with f=1, which marks a query
// that sets them).
func (p feedPrefs) Query() string {
	v := url.Values{"f": {"1"}, "show": {p.Show}}
	if p.Latest {
		v.Set("latest", "1")
	}
	if p.HideKerchunks {
		v.Set("hide", "1")
	}
	return v.Encode()
}

// ShowLink is the query for the same prefs showing show instead.
func (p feedPrefs) ShowLink(show string) string {
	p.Show = show
	return "?" + p.Query()
}

// filter returns up to max of the entries (newest first) that the prefs
// show.
func (p feedPrefs) filter(entries []activity.Entry, max int) []activity.Entry {
	var out []activity.Entry
	seen := map[string]bool{}
	for _, e := range entries {
		switch {
		case p.Show == "rf" && e.Source != "rf",
			p.Show == "net" && e.Source == "rf",
			p.Show == "sms" && e.Text == nil,
			p.HideKerchunks && isKerchunk(e):
			continue
		}
		if p.Latest {
			key := strings.ToUpper(strings.TrimSpace(e.Src)) + " " + entryMode(e)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, e)
		if len(out) == max {
			break
		}
	}
	return out
}

func isKerchunk(e activity.Entry) bool {
	return e.Kind == "voice" && !e.Active && e.Duration != nil && *e.Duration < kerchunk
}

// entryMode names what an entry is: Voice, or a packet's kind (SMS, APRS
// and so on, from its M17 packet protocol).
func entryMode(e activity.Entry) string {
	if e.Kind == "voice" {
		return "Voice"
	}
	if e.Text != nil {
		return "SMS"
	}
	if e.Protocol != nil {
		switch *e.Protocol {
		case 0:
			return "Raw"
		case 1:
			return "AX.25"
		case 2:
			return "APRS"
		case 3:
			return "6LoWPAN"
		case 4:
			return "IPv4"
		case 5:
			return "SMS"
		case 6:
			return "Winlink"
		}
	}
	return "Data"
}

// qrzURL is a callsign's QRZ page: its base call, without a suffix
// (N1ADJ/P, N1ADJ-7) or the M17 padding.
func qrzURL(call string) string {
	base := strings.TrimSpace(call)
	if i := strings.IndexAny(base, "/- "); i > 0 {
		base = base[:i]
	}
	return "https://www.qrz.com/db/" + url.PathEscape(strings.ToUpper(base))
}

// isCallsign: worth a QRZ link (not ALL, @ECHO, a reflector and so on).
func isCallsign(call string) bool {
	c := strings.TrimSpace(call)
	if c == "" || strings.HasPrefix(c, "@") || strings.HasPrefix(c, "M17-") || strings.EqualFold(c, "ALL") {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, r := range c {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

// ago is how long before now t was, briefly ("now", "33 s ago", "5 min
// ago", "2 h ago", then the date). The page's script keeps it current.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "now"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	}
	return t.Local().Format("Jan 2")
}
