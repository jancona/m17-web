package platform

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Problem is something wrong with the hotspot, for the health cards on
// the admin pages.
type Problem struct {
	Level  string // "error", "warn" or "info"
	Title  string
	Detail string // e.g. the gateway's last error line ("" if none)
	Page   string // the admin page that helps ("" for none)
}

// Health limits.
const (
	// restartsWindow and restartsMax: this many gateway starts in the
	// window is a restart loop
	restartsWindow = 10 * time.Minute
	restartsMax    = 3
	// modemGrace: a gateway running this long without its modem has a
	// problem rather than still starting
	modemGrace = 30 * time.Second
	// reflectorGrace: likewise for reaching its reflector
	reflectorGrace = 60 * time.Second
	hotC           = 80.0
	lowDiskBytes   = 200 << 20
)

var (
	gwTimeRE      = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})`)
	gwConnectedRE = regexp.MustCompile(`Connected to .* modem`)
	gwReachedRE   = regexp.MustCompile(`Reached reflector (\S+)`)
	gwNoReachRE   = regexp.MustCompile(`Can't reach reflector (\S+)`)
)

// gatewayLogState is what the gateway's log says about its last start.
type gatewayLogState struct {
	Starts      []time.Time // every start in the log
	LastStart   time.Time
	Connected   bool   // its modem, since the last start
	LastError   string // the last error line (across restarts)
	Reflector   string // the reflector it last tried to reach, since the last start
	Unreachable bool   // and it hasn't reached it
}

// readGatewayLog reads the gateway's log lines. Times are local, as the
// gateway writes them.
func readGatewayLog(lines []string) gatewayLogState {
	var st gatewayLogState
	for _, l := range lines {
		var t time.Time
		if m := gwTimeRE.FindStringSubmatch(l); m != nil {
			t, _ = time.ParseInLocation("2006/01/02 15:04:05", m[1], time.Local)
		}
		switch {
		case strings.Contains(l, "Loading settings from"):
			st.Starts = append(st.Starts, t)
			st.LastStart = t
			st.Connected, st.Reflector, st.Unreachable = false, "", false
		case gwConnectedRE.MatchString(l):
			st.Connected = true
		case gwReachedRE.MatchString(l):
			st.Reflector, st.Unreachable = gwReachedRE.FindStringSubmatch(l)[1], false
		case gwNoReachRE.MatchString(l):
			st.Reflector, st.Unreachable = gwNoReachRE.FindStringSubmatch(l)[1], true
		}
		if strings.Contains(l, "[ERROR]") || strings.Contains(l, "Error ") {
			st.LastError = l
		}
	}
	return st
}

// gatewayProblems turns the log state into problems, at now; running says
// whether a gateway process exists, and age how long it has run.
func gatewayProblems(st gatewayLogState, running bool, age time.Duration, now time.Time) []Problem {
	var ps []Problem
	recent := 0
	for _, t := range st.Starts {
		if !t.IsZero() && now.Sub(t) < restartsWindow {
			recent++
		}
	}
	switch {
	case !running:
		ps = append(ps, Problem{Level: "error", Title: "The gateway isn't running.", Detail: st.LastError, Page: "logs"})
	case recent >= restartsMax:
		ps = append(ps, Problem{Level: "error", Title: "The gateway keeps restarting: it started " + strconv.Itoa(recent) + " times in the last 10 minutes.", Detail: st.LastError, Page: "logs"})
	case !st.Connected && age > modemGrace:
		ps = append(ps, Problem{Level: "error", Title: "The gateway can't reach its radio HAT.", Detail: st.LastError, Page: "radio"})
	}
	if running && st.Connected && st.Unreachable && age > reflectorGrace {
		ps = append(ps, Problem{Level: "warn", Title: "The gateway can't reach the reflector " + st.Reflector + ". It keeps trying.", Page: "station"})
	}
	return ps
}

// HealthFiles are the files Health reads; unset ones are the standard
// paths. Tests replace them.
type HealthFiles struct {
	Runlevel string // the gateway's default-runlevel link
	RunMode  string // m17os-netwatch's mode ("ap" while the setup access point is up)
}

func (a *Alpine) healthFiles() HealthFiles {
	h := a.HealthPaths
	if h.Runlevel == "" {
		h.Runlevel = "/etc/runlevels/default/m17-gateway"
	}
	if h.RunMode == "" {
		h.RunMode = "/run/m17os/mode"
	}
	return h
}

// processAge finds the process named comm in procDir and how long it has
// run (ok false if there's none).
func processAge(procDir, comm string) (time.Duration, bool) {
	up := strings.Fields(readTrimmed(filepath.Join(procDir, "uptime")))
	if len(up) == 0 {
		return 0, false
	}
	uptime, _ := strconv.ParseFloat(up[0], 64)
	dirs, _ := os.ReadDir(procDir)
	for _, d := range dirs {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		if readTrimmed(filepath.Join(procDir, d.Name(), "comm")) != comm {
			continue
		}
		stat := readTrimmed(filepath.Join(procDir, d.Name(), "stat"))
		i := strings.LastIndex(stat, ") ")
		if i < 0 {
			continue
		}
		f := strings.Fields(stat[i+2:])
		if len(f) < 20 {
			continue
		}
		start, _ := strconv.ParseFloat(f[19], 64) // field 22: start time in ticks
		return time.Duration((uptime - start/100) * float64(time.Second)), true
	}
	return 0, false
}

func (a *Alpine) Health(ctx context.Context) ([]Problem, error) {
	hf, f := a.healthFiles(), a.systemFiles()
	var ps []Problem
	if readTrimmed(hf.RunMode) == "ap" {
		ps = append(ps, Problem{Level: "warn", Title: "The hotspot has no network: its setup access point is on.", Page: "network"})
	}
	// The gateway, if it's meant to run and is set up
	cur, _ := a.Current(ctx)
	if _, err := os.Lstat(hf.Runlevel); err == nil && cur.Callsign != "" {
		proc := filepath.Dir(f.Uptime)
		age, running := processAge(proc, "m17-gateway")
		ps = append(ps, gatewayProblems(readGatewayLog(tail(f.GatewayLog, 2000)), running, age, time.Now())...)
	}
	// An update waiting for a restart, or a new version being checked
	if st, err := a.OSStatus(ctx); err == nil {
		ps = append(ps, osProblems(st)...)
	}
	// Storage and temperature
	si, _ := a.SystemInfo(ctx)
	ps = append(ps, systemProblems(si)...)
	return ps, nil
}

// osProblems: a new version being checked, or installed and waiting for a
// restart.
func osProblems(st OSStatus) []Problem {
	var ps []Problem
	switch v := st.AwaitingRestart(); {
	case v != "":
		ps = append(ps, Problem{Level: "info", Title: "System version " + v + " is installed: restart to start it.", Page: "updates"})
	case st.Checking():
		ps = append(ps, Problem{Level: "info", Title: "Checking that this new system version starts properly (try " + strconv.Itoa(st.BootTries) + ").", Page: "updates"})
	}
	return ps
}

// systemProblems: storage nearly full, the CPU hot.
func systemProblems(si SystemInfo) []Problem {
	var ps []Problem
	if si.DiskTotal > 0 && si.DiskFree < lowDiskBytes {
		ps = append(ps, Problem{Level: "warn", Title: "Storage is nearly full: " + strconv.FormatUint(si.DiskFree>>20, 10) + " MB free.", Page: "system"})
	}
	if si.HasTemp && si.TempC >= hotC {
		ps = append(ps, Problem{Level: "warn", Title: "The CPU is hot: " + strconv.Itoa(int(si.TempC)) + " °C. Give the hotspot more air.", Page: "system"})
	}
	return ps
}
