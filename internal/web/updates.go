package web

import (
	"strings"

	"github.com/jancona/m17-web/internal/platform"
)

// updatesView is what the Updates card shows, worked out from the update
// jobs: "check" (OS and packages; m17os-autoupdate --check), "os-check"
// (OS only: images whose admin helper has no "check" job), "os-install"
// and "packages".
type updatesView struct {
	// Busy is the job running ("" if none) and its log
	Busy    string // "check", "os-install" or "packages"
	BusyLog []string

	// From the latest check, unless something installed since makes it
	// stale. Checked is false when there's no current check result.
	Checked           bool
	OSAvailable       string // the version available ("" if none)
	PackagesAvailable string // "m17-gateway 0.10.1, ..." ("" if none)
	// PackagesUnknown: the check said nothing about packages (OS-only
	// check), so Update packages is offered anyway
	PackagesUnknown bool
	CheckError      string
	CheckLog        []string

	// Results of the last installs, when newer than the check
	InstallFailed  bool
	InstallLog     []string
	PackagesResult string // packagesResult's summary ("" if none)
	PackagesFailed bool
	PackagesLog    []string
}

// UpToDate reports whether the check found nothing to install.
func (v updatesView) UpToDate() bool {
	return v.Checked && v.CheckError == "" && v.OSAvailable == "" && v.PackagesAvailable == "" && !v.PackagesUnknown
}

// buildUpdatesView works out the card from the jobs' statuses.
func buildUpdatesView(check, osCheck, install, packages platform.JobStatus) updatesView {
	var v updatesView
	for _, j := range []struct {
		name string
		s    platform.JobStatus
	}{{"check", check}, {"check", osCheck}, {"os-install", install}, {"packages", packages}} {
		if j.s.Running() {
			v.Busy, v.BusyLog = j.name, j.s.Log
			return v
		}
	}

	// The latest check: the combined one, or an OS-only one from an older
	// helper
	c, osOnly := check, false
	if osCheck.State != "" && osCheck.Started > check.Started {
		c, osOnly = osCheck, true
	}
	if c.State != "" {
		v.Checked = true
		v.CheckLog = c.Log
		switch {
		case c.State == "failed":
			v.CheckError = "The check didn't work. Is the hotspot online?"
		case osOnly:
			v.OSAvailable = updateAvailable(c)
			if c.Exit != 0 {
				v.OSAvailable = ""
			}
			v.PackagesUnknown = true
		default:
			for _, l := range c.Log {
				k, val, _ := strings.Cut(l, "=")
				switch k {
				case "OS_AVAILABLE":
					v.OSAvailable = val
				case "PACKAGES_AVAILABLE":
					v.PackagesAvailable = val
				case "OS_ERROR", "PACKAGES_ERROR":
					v.CheckError = "Part of the check didn't work: " + val
				}
			}
		}
	}

	// Installs since the check settle what it found
	if install.State != "" && install.Started >= c.Started {
		v.OSAvailable = ""
		if install.State == "failed" {
			v.InstallFailed, v.InstallLog = true, install.Log
		}
	}
	if packages.State != "" && packages.Started >= c.Started {
		v.PackagesAvailable, v.PackagesUnknown = "", false
		v.PackagesResult = packagesResult(packages)
		v.PackagesFailed = packages.State == "failed"
		v.PackagesLog = packages.Log
	}
	return v
}
