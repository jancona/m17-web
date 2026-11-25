package web

import (
	"testing"

	"github.com/jancona/m17-web/internal/platform"
)

func TestBuildUpdatesView(t *testing.T) {
	ok := func(started int64, log ...string) platform.JobStatus {
		return platform.JobStatus{State: "ok", Started: started, Log: log}
	}
	none := platform.JobStatus{}
	both := ok(100, "OS_AVAILABLE=2026.11.27.1", "PACKAGES_AVAILABLE=m17-gateway 0.10.1-r0")

	v := buildUpdatesView(both, none, none, none)
	if v.OSAvailable != "2026.11.27.1" || v.PackagesAvailable != "m17-gateway 0.10.1-r0" || v.UpToDate() {
		t.Errorf("both available: %+v", v)
	}
	if v := buildUpdatesView(ok(100, "OS_CURRENT=true", "PACKAGES_CURRENT=true"), none, none, none); !v.UpToDate() {
		t.Errorf("nothing new: %+v", v)
	}
	// Something running wins
	if v := buildUpdatesView(both, none, platform.JobStatus{State: "running", Started: 200}, none); v.Busy != "os-install" {
		t.Errorf("installing: %+v", v)
	}
	// An install after the check settles the OS part; the packages part stays
	if v := buildUpdatesView(both, none, ok(150), none); v.OSAvailable != "" || v.PackagesAvailable == "" {
		t.Errorf("after the OS install: %+v", v)
	}
	// A package update after the check settles the packages part
	v = buildUpdatesView(both, none, none, ok(150, "updated packages: m17-gateway 0.10.1-r0"))
	if v.PackagesAvailable != "" || v.PackagesResult != "Updated: m17-gateway 0.10.1-r0" || v.OSAvailable == "" {
		t.Errorf("after the package update: %+v", v)
	}
	// A check newer than an old install shows what it found
	if v := buildUpdatesView(ok(300, "OS_AVAILABLE=2026.12.1.1", "PACKAGES_CURRENT=true"), none, ok(150), none); v.OSAvailable != "2026.12.1.1" {
		t.Errorf("new check after an old install: %+v", v)
	}
	// An OS-only check (older helper): packages unknown, so offered
	v = buildUpdatesView(none, platform.JobStatus{State: "ok", Exit: 1, Started: 100, Log: []string{"Status:     up to date"}}, none, none)
	if v.OSAvailable != "" || !v.PackagesUnknown || v.UpToDate() {
		t.Errorf("OS-only, up to date: %+v", v)
	}
	if v := buildUpdatesView(platform.JobStatus{State: "failed", Started: 100}, none, none, none); v.CheckError == "" {
		t.Errorf("failed check: %+v", v)
	}
}
