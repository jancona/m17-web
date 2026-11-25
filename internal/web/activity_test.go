package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jancona/m17-web/internal/activity"
	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/wizard"
)

func activityServer(t *testing.T, act *activity.Log) *httptest.Server {
	t.Helper()
	srv, err := New(platform.NewFake(), wizard.NewStore(t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if act != nil {
		srv.SetActivity(act)
	}
	ts := httptest.NewUnstartedServer(srv)
	// Like the real server's, but short, so the event stream test sees it
	ts.Config.WriteTimeout = time.Second
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func fetch(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The activity page and API need no login, and show the gateway's session
func TestActivity(t *testing.T) {
	act := activity.New("", 0)
	f, err := os.Open("../activity/testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// Leave out the shutdown at the end, so the link is up
		if strings.Contains(sc.Text(), "stopping") {
			break
		}
		if err := act.Handle(sc.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	ts := activityServer(t, act)

	code, body := fetch(t, ts.URL+"/dashboard/")
	if code != 200 {
		t.Fatalf("GET /dashboard/: %d", code)
	}
	for _, want := range []string{"can&#39;t link to M17-M17 C (rejected)", "N1ADJ", "AA5RL", "“hi”", "81DEAD",
		"6.0 s", "1.5%", "late entry", "📍", "Not connected to the hotspot's MQTT broker",
		`<noscript><meta http-equiv="refresh" content="15"></noscript>`, `data-events="/dashboard/events"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard has no %q", want)
		}
	}
	// The live part alone, as the page's script fetches it
	code, live := fetch(t, ts.URL+"/dashboard/live")
	if code != 200 || !strings.Contains(live, "Last heard") || strings.Contains(live, "<html") {
		t.Errorf("live part: %d\n%s", code, live)
	}
	// The messages page: text messages only, escaped
	if err := act.Handle([]byte(`{"M17Packet":{"timestamp":"2026-11-20T01:10:00Z","source":"network","src_callsign":"AA5RL","dst_callsign":"N1ADJ","protocol":5,"text":"<b>73</b>"}}`)); err != nil {
		t.Fatal(err)
	}
	code, msgs := fetch(t, ts.URL+"/dashboard/messages")
	if code != 200 {
		t.Fatalf("GET /dashboard/messages: %d", code)
	}
	for _, want := range []string{"<h1>Messages</h1>", ">hi<", "&lt;b&gt;73&lt;/b&gt;", "PARROT", `data-live="/dashboard/messages/live"`,
		`<a href="/dashboard/messages" aria-current="page">`, "The last 2 text messages"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages page has no %q", want)
		}
	}
	if strings.Contains(msgs, "81DEAD") || strings.Contains(msgs, "<b>73") {
		t.Errorf("messages page has a data packet or unescaped text:\n%s", msgs)
	}
	if code, live := fetch(t, ts.URL+"/dashboard/messages/live"); code != 200 || !strings.Contains(live, "&lt;b&gt;73") || strings.Contains(live, "<html") {
		t.Errorf("messages live part: %d\n%s", code, live)
	}
	// The map: a recent position (the session's own is dated, so the map
	// stops showing it 30 days after)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := act.Handle([]byte(`{"M17GNSS":{"timestamp":"` + recent + `","source":"network","src_callsign":"W1AW","latitude":41.71463,"longitude":-72.72708,"altitude":30,"speed":62,"bearing":35,"station_type":1}}`)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/dashboard/map")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	mapPage := string(b)
	if c := resp.Header.Get("Content-Security-Policy"); !strings.Contains(c, "img-src 'self' data: https://tile.openstreetmap.org;") || strings.Contains(c, "script-src 'self' http") {
		t.Errorf("map CSP: %q", c)
	}
	for _, want := range []string{`data-positions="/dashboard/positions.json"`, `data-tiles="https://tile.openstreetmap.org/{z}/{x}/{y}.png"`,
		`href="/setup/static/leaflet/leaflet.css"`, `src="/setup/static/leaflet/leaflet.js"`, `data-live="/dashboard/map/live"`,
		"W1AW", "mobile", "41.71463, -72.72708", "30 m (98 ft)", "62 km/h (39 mph) heading 35°"} {
		if !strings.Contains(mapPage, want) {
			t.Errorf("map page has no %q", want)
		}
	}
	// Other pages keep the strict policy
	if resp, err := http.Get(ts.URL + "/dashboard/"); err == nil {
		resp.Body.Close()
		if strings.Contains(resp.Header.Get("Content-Security-Policy"), "openstreetmap") {
			t.Error("the status page allows map tiles")
		}
	}
	code, pj := fetch(t, ts.URL+"/dashboard/positions.json")
	var ps []activity.StationPosition
	if err := json.Unmarshal([]byte(pj), &ps); code != 200 || err != nil || len(ps) == 0 || ps[0].Callsign != "W1AW" || *ps[0].Bearing != 35 {
		t.Errorf("positions.json: %d %v %s", code, err, pj)
	}
	if code, live := fetch(t, ts.URL+"/dashboard/map/live"); code != 200 || !strings.Contains(live, "W1AW") || strings.Contains(live, "<html") {
		t.Errorf("map live part: %d\n%s", code, live)
	}
	// The old activity page now redirects
	if code, body := fetch(t, ts.URL+"/setup/activity"); code != 200 || !strings.Contains(body, "Last heard") {
		t.Errorf("/setup/activity: %d", code)
	}

	code, body = fetch(t, ts.URL+"/setup/api/activity")
	if code != 200 {
		t.Fatalf("GET /setup/api/activity: %d", code)
	}
	var got struct {
		Status  activity.Status  `json:"status"`
		Entries []activity.Entry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 5 || got.Entries[4].Src != "N1ADJ" || got.Status.Link.Reason != "rejected" || got.Status.ModemOpen == nil || !*got.Status.ModemOpen {
		t.Errorf("API: %s", body)
	}
}

// TestNoActivity: started without a broker, the page says live status is
// off, and the API isn't there.
func TestNoActivity(t *testing.T) {
	ts := activityServer(t, nil)
	if code, body := fetch(t, ts.URL+"/dashboard/positions.json"); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("positions.json without a broker: %d %q", code, body)
	}
	for _, p := range []string{"/dashboard/", "/dashboard/messages", "/dashboard/map"} {
		if code, body := fetch(t, ts.URL+p); code != 200 || !strings.Contains(body, "Live status is off") || strings.Contains(body, `data-events="/`) {
			t.Errorf("%s without a broker: %d\n%s", p, code, body)
		}
	}
	for _, p := range []string{"/setup/api/activity", "/dashboard/events"} {
		if code, _ := fetch(t, ts.URL+p); code != 404 {
			t.Errorf("%s without a broker: %d, want 404", p, code)
		}
	}
}

// TestDashboardEvents: the event stream sends an update when the gateway
// reports something.
func TestDashboardEvents(t *testing.T) {
	act := activity.New("", 0)
	ts := activityServer(t, act)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/dashboard/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type %q", ct)
	}
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() == "event: update" {
				got <- sc.Text()
				return
			}
		}
	}()
	// Past the server's write timeout: the stream must outlast it
	time.Sleep(1500 * time.Millisecond)
	if err := act.Handle([]byte(`{"MMDVM":{"timestamp":"2026-11-25T12:00:00Z","mode":"M17"}}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("no update event")
	}
}
