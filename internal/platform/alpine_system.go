package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SystemFiles are the files the Alpine platform reads for the admin pages;
// they can be replaced in tests.
type SystemFiles struct {
	Model      string // the board's model
	OSRelease  string // m17-hotspot-os's release file
	Kernel     string
	Uptime     string
	Temp       string // millidegrees C
	MemInfo    string
	LoadAvg    string
	DataDir    string // where the disk space is measured
	EventsLog  string
	GatewayLog string
	WebLog     string
}

func defaultSystemFiles() SystemFiles {
	return SystemFiles{
		Model:      "/proc/device-tree/model",
		OSRelease:  "/etc/m17os-release",
		Kernel:     "/proc/sys/kernel/osrelease",
		Uptime:     "/proc/uptime",
		Temp:       "/sys/class/thermal/thermal_zone0/temp",
		MemInfo:    "/proc/meminfo",
		LoadAvg:    "/proc/loadavg",
		DataDir:    "/data",
		EventsLog:  "/data/log/m17os-events.log",
		GatewayLog: "/var/log/m17-gateway/m17-gateway.log",
		WebLog:     "/var/log/m17-web/m17-web.log",
	}
}

// systemFiles returns a's files, defaulting each unset one.
func (a *Alpine) systemFiles() SystemFiles {
	f, d := a.Files, defaultSystemFiles()
	set := func(p *string, def string) {
		if *p == "" {
			*p = def
		}
	}
	set(&f.Model, d.Model)
	set(&f.OSRelease, d.OSRelease)
	set(&f.Kernel, d.Kernel)
	set(&f.Uptime, d.Uptime)
	set(&f.Temp, d.Temp)
	set(&f.MemInfo, d.MemInfo)
	set(&f.LoadAvg, d.LoadAvg)
	set(&f.DataDir, d.DataDir)
	set(&f.EventsLog, d.EventsLog)
	set(&f.GatewayLog, d.GatewayLog)
	set(&f.WebLog, d.WebLog)
	return f
}

// unsupported turns the helper's "unknown command" (an image from before
// the command) into ErrUnsupported.
func unsupported(err error) error {
	if err != nil && strings.Contains(err.Error(), "unknown command") {
		return ErrUnsupported
	}
	return err
}

func (a *Alpine) SystemInfo(ctx context.Context) (SystemInfo, error) {
	f := a.systemFiles()
	var si SystemInfo
	si.Board = strings.TrimRight(readTrimmed(f.Model), "\x00")
	si.Hostname = readTrimmed(a.HostnameFile)
	si.Kernel = readTrimmed(f.Kernel)
	if b, err := os.ReadFile(f.OSRelease); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "Version:"); ok {
				si.OSVersion = strings.TrimSpace(v)
			}
		}
	}
	if up := strings.Fields(readTrimmed(f.Uptime)); len(up) > 0 {
		if secs, err := strconv.ParseFloat(up[0], 64); err == nil {
			si.Uptime = time.Duration(secs) * time.Second
		}
	}
	if t, err := strconv.Atoi(readTrimmed(f.Temp)); err == nil {
		si.TempC, si.HasTemp = float64(t)/1000, true
	}
	si.MemTotal, si.MemAvailable = memInfo(f.MemInfo)
	si.DiskTotal, si.DiskFree = diskSpace(f.DataDir)
	if l := strings.Fields(readTrimmed(f.LoadAvg)); len(l) >= 3 {
		si.Load = strings.Join(l[:3], " ")
	}
	si.Addresses = addresses()
	si.Modem, si.ModemIdent, si.ModemAt = a.gatewayStart()
	return si, nil
}

// memInfo reads MemTotal and MemAvailable from /proc/meminfo, in bytes.
func memInfo(path string) (total, avail uint64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			avail = n * 1024
		}
	}
	return total, avail
}

// addresses are the hotspot's IP addresses, without loopback and
// link-local ones.
func addresses() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, i.Name+" "+ipn.IP.String())
		}
	}
	return out
}

// gatewayStart reads the gateway's last "Gateway Start" event from its
// dashboard log (the file the gateway INI's [General] DashboardLog names):
// the modem type and its description, and when.
func (a *Alpine) gatewayStart() (modem, ident string, at time.Time) {
	ini, err := readINI(a.GatewayINI)
	if err != nil {
		return
	}
	path := ini["General"]["DashboardLog"]
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join("/opt/m17/m17-gateway", path)
	}
	for _, line := range tail(path, 2000) {
		var ev struct {
			Time       time.Time `json:"time"`
			Type       string    `json:"type"`
			Subtype    string    `json:"subtype"`
			Modem      string    `json:"modem"`
			ModemIdent string    `json:"modemIdent"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "Gateway" && ev.Subtype == "Start" {
			modem, ident, at = ev.Modem, ev.ModemIdent, ev.Time
		}
	}
	return
}

// tail returns the last n lines of the file at path (none if it can't be
// read). It reads at most the last 512 KiB.
func tail(path string, n int) []string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	const max = 512 << 10
	if st, err := fh.Stat(); err == nil && st.Size() > max {
		_, _ = fh.Seek(st.Size()-max, io.SeekStart)
	}
	var lines []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 2*n {
			lines = append(lines[:0], lines[len(lines)-n:]...)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (a *Alpine) SavedWiFi(ctx context.Context) ([]SavedNetwork, error) {
	out, err := a.helper(ctx, "", "wifi-networks")
	if err != nil {
		return nil, unsupported(err)
	}
	var nets []SavedNetwork
	for _, line := range strings.Split(out, "\n") {
		p := strings.SplitN(line, "\t", 3)
		if len(p) != 3 || p[2] == "" {
			continue
		}
		prio, _ := strconv.Atoi(p[0])
		nets = append(nets, SavedNetwork{SSID: p[2], Priority: prio, Open: p[1] == "open"})
	}
	return nets, nil
}

func (a *Alpine) WiFiStatus(ctx context.Context) (WiFiStatus, error) {
	out, err := a.helper(ctx, "", "wifi-status")
	if err != nil {
		return WiFiStatus{}, unsupported(err)
	}
	kv, _ := parseKV(out)
	w := WiFiStatus{State: kv["wpa_state"], SSID: kv["ssid"], Address: kv["ip_address"]}
	w.Signal, _ = strconv.Atoi(kv["signal"])
	w.FreqMHz, _ = strconv.Atoi(kv["freq"])
	return w, nil
}

func (a *Alpine) AddWiFi(ctx context.Context, ssid, password string) error {
	_, err := a.helper(ctx, "ssid="+ssid+"\npassword="+password+"\n", "wifi-add")
	return unsupported(err)
}

func (a *Alpine) RemoveWiFi(ctx context.Context, ssid string) error {
	_, err := a.helper(ctx, "", "wifi-remove", ssid)
	return unsupported(err)
}

func (a *Alpine) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	out, err := a.helper(ctx, "", "ssh-keys")
	if err != nil {
		return nil, unsupported(err)
	}
	var keys []SSHKey
	for _, line := range strings.Split(out, "\n") {
		p := strings.SplitN(line, "\t", 3)
		if len(p) < 2 || p[1] == "" {
			continue
		}
		k := SSHKey{Type: p[0], Key: p[1]}
		if len(p) == 3 {
			k.Comment = p[2]
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (a *Alpine) RemoveSSHKey(ctx context.Context, key string) error {
	_, err := a.helper(ctx, "", "ssh-key-remove", key)
	return unsupported(err)
}

func (a *Alpine) OSStatus(ctx context.Context) (OSStatus, error) {
	out, err := a.helper(ctx, "", "os-status")
	if err != nil {
		return OSStatus{}, unsupported(err)
	}
	return parseOSStatus(out), nil
}

// parseOSStatus reads the helper's os-status: m17os-slot-info's JSON line,
// then update.KEY=VALUE lines.
func parseOSStatus(out string) OSStatus {
	var st OSStatus
	st.LastUpdate = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "{"):
			var j struct {
				Active   string `json:"active_slot"`
				Previous string `json:"previous_slot"`
				Pending  bool   `json:"pending"`
				Tries    int    `json:"boot_tries"`
				A        string `json:"slot_a_version"`
				B        string `json:"slot_b_version"`
				Running  string `json:"running_version"`
			}
			if json.Unmarshal([]byte(line), &j) == nil {
				st.ActiveSlot, st.PreviousSlot, st.Pending, st.BootTries = j.Active, j.Previous, j.Pending, j.Tries
				st.SlotA, st.SlotB, st.Running = j.A, j.B, j.Running
			}
		case strings.HasPrefix(line, "update."):
			if k, v, ok := strings.Cut(strings.TrimPrefix(line, "update."), "="); ok {
				st.LastUpdate[k] = v
			}
		}
	}
	switch st.PreviousSlot {
	case "A":
		st.PreviousVersion = st.SlotA
	case "B":
		st.PreviousVersion = st.SlotB
	}
	if st.PreviousSlot == st.ActiveSlot {
		st.PreviousVersion = ""
	}
	return st
}

func (a *Alpine) StartJob(ctx context.Context, name string) error {
	_, err := a.helper(ctx, "", "job-start", name)
	return unsupported(err)
}

func (a *Alpine) JobStatus(ctx context.Context, name string) (JobStatus, error) {
	out, err := a.helper(ctx, "", "job-status", name)
	if err != nil {
		if unsupported(err) == ErrUnsupported {
			return JobStatus{}, nil
		}
		return JobStatus{}, err
	}
	kv, log := parseKV(out)
	j := JobStatus{State: kv["state"], Log: log}
	j.Exit, _ = strconv.Atoi(kv["exit"])
	j.Started, _ = strconv.ParseInt(kv["started"], 10, 64)
	return j, nil
}

func (a *Alpine) Rollback(ctx context.Context) error {
	_, err := a.helper(ctx, "", "rollback")
	return unsupported(err)
}

func (a *Alpine) Log(ctx context.Context, name string) ([]string, error) {
	f := a.systemFiles()
	const n = 300
	switch name {
	case "events":
		return tail(f.EventsLog, n), nil
	case "gateway":
		return tail(f.GatewayLog, n), nil
	case "web":
		return tail(f.WebLog, n), nil
	case "system":
		out, err := a.helper(ctx, "", "syslog")
		if err != nil {
			return nil, unsupported(err)
		}
		return strings.Split(strings.TrimRight(out, "\n"), "\n"), nil
	}
	return nil, os.ErrNotExist
}

func (a *Alpine) Reboot(ctx context.Context) error {
	_, err := a.helper(ctx, "", "reboot")
	return unsupported(err)
}

func (a *Alpine) Shutdown(ctx context.Context) error {
	_, err := a.helper(ctx, "", "shutdown")
	return unsupported(err)
}
