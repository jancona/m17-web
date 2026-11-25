package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jancona/m17-web/internal/activity"
	"github.com/jancona/m17-web/internal/platform"
)

func ptr[T any](v T) *T { return &v }

// sample entries, newest first
func sampleEntries() []activity.Entry {
	return []activity.Entry{
		{Kind: "voice", Source: "network", Src: "AA5RL", Dst: "M17-M17 T", Active: true},
		{Kind: "voice", Source: "rf", Src: "N1ADJ", Dst: "M17-M17 T", Duration: ptr(8.4)},
		{Kind: "packet", Source: "network", Src: "N1ADJ", Dst: "@ECHO", Protocol: ptr(5), Text: ptr("hi")},
		{Kind: "packet", Source: "rf", Src: "N1ADJ", Dst: "@ECHO", Protocol: ptr(5), Text: ptr("hi")},
		{Kind: "voice", Source: "network", Src: "W1AW", Dst: "ALL", Duration: ptr(0.6)}, // a kerchunk
		{Kind: "packet", Source: "network", Src: "DK1MI", Dst: "ALL", Protocol: ptr(2), Data: "82"},
		{Kind: "voice", Source: "rf", Src: "N1ADJ  ", Dst: "ALL", Duration: ptr(6.0)},
		{Kind: "voice", Source: "network", Src: "AA5RL", Dst: "M17-M17 T", Duration: ptr(22.0)},
	}
}

func calls(es []activity.Entry) string {
	var b []string
	for _, e := range es {
		b = append(b, strings.TrimSpace(e.Src)+"/"+entryMode(e)+"/"+e.Source)
	}
	return strings.Join(b, " ")
}

func TestFeedFilter(t *testing.T) {
	es := sampleEntries()
	for _, c := range []struct {
		p    feedPrefs
		max  int
		want string
	}{
		{feedPrefs{Show: "all"}, 50, "AA5RL/Voice/network N1ADJ/Voice/rf N1ADJ/SMS/network N1ADJ/SMS/rf W1AW/Voice/network DK1MI/APRS/network N1ADJ/Voice/rf AA5RL/Voice/network"},
		{feedPrefs{Show: "all"}, 2, "AA5RL/Voice/network N1ADJ/Voice/rf"},
		{feedPrefs{Show: "all", HideKerchunks: true}, 50, "AA5RL/Voice/network N1ADJ/Voice/rf N1ADJ/SMS/network N1ADJ/SMS/rf DK1MI/APRS/network N1ADJ/Voice/rf AA5RL/Voice/network"},
		{feedPrefs{Show: "rf"}, 50, "N1ADJ/Voice/rf N1ADJ/SMS/rf N1ADJ/Voice/rf"},
		{feedPrefs{Show: "net"}, 50, "AA5RL/Voice/network N1ADJ/SMS/network W1AW/Voice/network DK1MI/APRS/network AA5RL/Voice/network"},
		{feedPrefs{Show: "sms"}, 50, "N1ADJ/SMS/network N1ADJ/SMS/rf"},
		// The latest per station and mode; the one on air counts, and
		// padding doesn't make another station
		{feedPrefs{Show: "all", Latest: true, HideKerchunks: true}, 50, "AA5RL/Voice/network N1ADJ/Voice/rf N1ADJ/SMS/network DK1MI/APRS/network"},
	} {
		if got := calls(c.p.filter(es, c.max)); got != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.p, got, c.want)
		}
	}
}

func TestFeedPrefs(t *testing.T) {
	// No query, no cookie: the defaults
	w := httptest.NewRecorder()
	if p := readFeedPrefs(w, httptest.NewRequest("GET", "/dashboard/", nil)); p != defaultPrefs || len(w.Result().Cookies()) != 0 {
		t.Errorf("defaults: %+v", p)
	}
	// A query sets them and the cookie; a missing checkbox is off
	w = httptest.NewRecorder()
	p := readFeedPrefs(w, httptest.NewRequest("GET", "/dashboard/?f=1&show=rf&latest=1", nil))
	if p != (feedPrefs{Show: "rf", Latest: true}) {
		t.Errorf("from the query: %+v", p)
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != prefsCookie || cs[0].Path != "/dashboard/" || !cs[0].HttpOnly {
		t.Fatalf("cookie: %+v", cs)
	}
	// The cookie brings them back
	r := httptest.NewRequest("GET", "/dashboard/", nil)
	r.AddCookie(cs[0])
	if p := readFeedPrefs(httptest.NewRecorder(), r); p != (feedPrefs{Show: "rf", Latest: true}) {
		t.Errorf("from the cookie: %+v", p)
	}
	// Nonsense is the default view
	if p := readFeedPrefs(httptest.NewRecorder(), httptest.NewRequest("GET", "/dashboard/?f=1&show=x", nil)); p.Show != "all" {
		t.Errorf("bad show: %+v", p)
	}
	if got := (feedPrefs{Show: "all", HideKerchunks: true}).ShowLink("sms"); got != "?f=1&hide=1&show=sms" {
		t.Errorf("ShowLink = %q", got)
	}
}

// TestDashboardFilters: the filter links and the cookie, through the server.
func TestDashboardFilters(t *testing.T) {
	act := activity.New("", 0)
	for _, m := range []string{
		`{"Modem":{"open":true,"description":"CC1200"}}`,
		`{"M17":{"timestamp":"2026-11-25T10:00:00Z","src_callsign":"N1ADJ","dst_callsign":"ALL","source":"rf","action":"start"}}`,
		`{"M17":{"timestamp":"2026-11-25T10:00:06Z","source":"rf","action":"end","duration":6,"ber":0.4}}`,
		`{"M17":{"timestamp":"2026-11-25T10:01:00Z","src_callsign":"DK1MI","dst_callsign":"M17-M17 T","source":"network","action":"start"}}`,
	} {
		if err := act.Handle([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	ts := activityServer(t, act)
	// DK1MI is on air: the card says so, with a running length
	_, body := fetch(t, ts.URL+"/dashboard/")
	// Network traffic is "Network", the reflector's and the hotspot's own
	for _, want := range []string{`aria-label="On air now"`, `class="caller live"`, `data-since="2026-11-25T10:01:00Z"`, "radio transmitting",
		"<dt>Via</dt><dd>Network</dd>", ">Network</a>"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard has no %q", want)
		}
	}
	// Radio only, kept in the cookie
	resp, err := http.Get(ts.URL + "/dashboard/?f=1&show=rf")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var prefs *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == prefsCookie {
			prefs = c
		}
	}
	if prefs == nil {
		t.Fatalf("no %s cookie: %+v", prefsCookie, resp.Cookies())
	}
	req, _ := http.NewRequest("GET", ts.URL+"/dashboard/live", nil)
	req.AddCookie(prefs)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	live := string(lb)
	// The card still shows DK1MI; the list has only N1ADJ
	table := live[strings.Index(live, "<table"):]
	if !strings.Contains(table, "N1ADJ") || strings.Contains(table, "DK1MI") || !strings.Contains(live, `aria-current="page">Radio`) {
		t.Errorf("radio only:\n%s", live)
	}
}

func TestEntryMode(t *testing.T) {
	for _, c := range []struct {
		e    activity.Entry
		want string
	}{
		{activity.Entry{Kind: "voice"}, "Voice"},
		{activity.Entry{Kind: "packet", Text: ptr("x"), Protocol: ptr(5)}, "SMS"},
		{activity.Entry{Kind: "packet", Protocol: ptr(2)}, "APRS"},
		{activity.Entry{Kind: "packet", Protocol: ptr(129)}, "Data"},
		{activity.Entry{Kind: "packet"}, "Data"},
	} {
		if got := entryMode(c.e); got != c.want {
			t.Errorf("entryMode(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestCallsigns(t *testing.T) {
	for call, want := range map[string]string{"N1ADJ": "N1ADJ", "N1ADJ    ": "N1ADJ", "n1adj/p": "N1ADJ", "AA5RL-7": "AA5RL", "N1ADJ X": "N1ADJ"} {
		if got := qrzURL(call); got != "https://www.qrz.com/db/"+want {
			t.Errorf("qrzURL(%q) = %q", call, got)
		}
	}
	for call, want := range map[string]bool{"N1ADJ": true, "ALL": false, "@ECHO": false, "M17-M17 T": false, "PARROT": false, "": false} {
		if got := isCallsign(call); got != want {
			t.Errorf("isCallsign(%q) = %v", call, got)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		2 * time.Second: "now", 33 * time.Second: "33 s ago", 5 * time.Minute: "5 min ago", 3 * time.Hour: "3 h ago",
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTxPower(t *testing.T) {
	for _, c := range []struct {
		cur  platform.Current
		want string
	}{
		{platform.Current{HATType: "cc1200", Power: 10}, "10 dBm (10 mW)"},
		{platform.Current{HATType: "cc1200", Power: 14}, "14 dBm (25 mW)"},
		{platform.Current{HATType: "cc1200", Power: -16}, "-16 dBm (0.025 mW)"},
		{platform.Current{HATType: "mmdvm", RFLevel: 40}, "power 40%"},
		{platform.Current{HATType: "sx1255", Power: 10}, ""},
		{platform.Current{}, ""},
	} {
		if got := txPower(c.cur); got != c.want {
			t.Errorf("txPower(%+v) = %q, want %q", c.cur, got, c.want)
		}
	}
}

// TestHelpPage: the help page is public, linked from every page, and
// rpi-dashboard's help.php leads to it.
func TestHelpPage(t *testing.T) {
	ts := activityServer(t, nil)
	code, body := fetch(t, ts.URL+"/dashboard/help")
	for _, want := range []string{"<h1>Help</h1>", `href="#questions"`, "dashboard_password=", "M17-Setup-", `href="/admin/station"`} {
		if !strings.Contains(body, want) {
			t.Errorf("help page has no %q", want)
		}
	}
	if code != 200 || strings.Contains(body, `http-equiv="refresh"`) {
		t.Errorf("help page: %d, or it refreshes itself", code)
	}
	if _, body := fetch(t, ts.URL+"/dashboard/"); !strings.Contains(body, `<a href="/dashboard/help">Help</a>`) {
		t.Error("the dashboard doesn't link to help")
	}
}
