package web

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
	ts := httptest.NewServer(srv)
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

	code, body := fetch(t, ts.URL+"/setup/activity")
	if code != 200 {
		t.Fatalf("GET /setup/activity: %d", code)
	}
	for _, want := range []string{"can&#39;t link to M17-M17 C (rejected)", "N1ADJ", "AA5RL", "“hi”", "81DEAD",
		"6.0 s", "1.5%", "late entry", "📍", "Not connected to the hotspot's MQTT broker", `http-equiv="refresh"`} {
		if !strings.Contains(body, want) {
			t.Errorf("activity page has no %q", want)
		}
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
	if len(got.Entries) != 4 || got.Entries[3].Src != "N1ADJ" || got.Status.Link.Reason != "rejected" || got.Status.ModemOpen == nil || !*got.Status.ModemOpen {
		t.Errorf("API: %s", body)
	}
}

// TestNoActivity: started without a broker, the page says live status is
// off, and the API isn't there.
func TestNoActivity(t *testing.T) {
	ts := activityServer(t, nil)
	if code, body := fetch(t, ts.URL+"/setup/activity"); code != 200 || !strings.Contains(body, "Live status is off") {
		t.Errorf("activity page without a broker: %d\n%s", code, body)
	}
	if code, _ := fetch(t, ts.URL+"/setup/api/activity"); code != 404 {
		t.Errorf("activity API without a broker: %d, want 404", code)
	}
}
