package platform

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jancona/m17-web/internal/firmware"
	"github.com/jancona/m17-web/internal/hat"
)

// Alpine is the Platform on m17-hotspot-os. It reads what it may read
// directly (the gateway's configuration, host files, time zones, the
// dashboard's password hash) and does the rest through m17-hotspot-os's
// admin helper, which m17-web may run as root through doas.
type Alpine struct {
	// Helper is the command that runs the admin helper.
	Helper []string
	// Files it reads.
	GatewayINI      string
	DashboardConfig string
	HostFiles       []string // later files override earlier ones
	ZoneTab         string
	HostnameFile    string
	TimeZoneFile    string
	// ScanCache holds the networks seen before the setup access point
	// started (the radio can't scan while it is an access point), in the
	// helper's wifi-scan format.
	ScanCache string
	// SetupAddress is the setup access point's own address: an interface
	// with it doesn't count as being online.
	SetupAddress string
	// FirmwareDir holds the firmware files (the m17-firmware packages).
	FirmwareDir string
	// Probe identifies the HAT on a serial port.
	Probe func(port string) (hat.Result, error)

	probeMu sync.Mutex
}

// NewAlpine returns the Platform for m17-hotspot-os, with its standard
// paths.
func NewAlpine() *Alpine {
	return &Alpine{
		Helper:          []string{"doas", "-n", "/usr/local/libexec/m17os/admin-helper"},
		GatewayINI:      "/etc/m17-gateway.ini",
		DashboardConfig: "/var/lib/m17-dashboard/config.php",
		HostFiles:       []string{"/var/lib/m17-gateway/M17Hosts.txt", "/var/lib/m17-gateway/OverrideHosts.txt"},
		ZoneTab:         "/usr/share/zoneinfo/zone1970.tab",
		HostnameFile:    "/etc/hostname",
		TimeZoneFile:    "/etc/timezone",
		ScanCache:       "/run/m17os/wifi-scan",
		SetupAddress:    "192.168.4.1",
		FirmwareDir:     "/usr/share/m17-firmware",
		Probe: func(port string) (hat.Result, error) {
			return hat.Probe(port, hat.OpenSerial)
		},
	}
}

// helper runs the admin helper with args, stdin as its input, and returns
// its output.
func (a *Alpine) helper(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, a.Helper[0], append(slices.Clone(a.Helper[1:]), args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("admin helper %s: %s", args[0], msg)
	}
	return out.String(), nil
}

// ── Current settings ──────────────────────────────────────────────────

// readINI reads an INI file into section -> key -> value. Comments start
// with ; or #.
func readINI(path string) (map[string]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ini := map[string]map[string]string{}
	sec := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			sec = strings.TrimSpace(line[1 : len(line)-1])
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if ini[sec] == nil {
				ini[sec] = map[string]string{}
			}
			ini[sec][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return ini, sc.Err()
}

func readTrimmed(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

func (a *Alpine) Current(ctx context.Context) (Current, error) {
	c := Current{
		Hostname:   readTrimmed(a.HostnameFile),
		TimeZone:   readTrimmed(a.TimeZoneFile),
		AutoUpdate: "auto",
		UpdateHour: 3,
		Online:     a.online(),
	}
	if c.TimeZone == "" {
		c.TimeZone = "UTC"
	}
	ini, err := readINI(a.GatewayINI)
	if err != nil {
		return c, fmt.Errorf("reading the gateway configuration: %w", err)
	}
	if cs := ini["General"]["Callsign"]; !strings.Contains(cs, "PLACEHOLDER") {
		c.Callsign = cs
	}
	c.HATType = strings.ToLower(ini["Modem"]["Type"])
	if strings.HasPrefix(c.HATType, "cc1200") {
		c.HATType = "cc1200"
	}
	c.RXFrequency, _ = strconv.ParseInt(ini["Radio"]["RXFrequency"], 10, 64)
	c.TXFrequency, _ = strconv.ParseInt(ini["Radio"]["TXFrequency"], 10, 64)
	c.Duplex = strings.EqualFold(ini["Radio"]["Duplex"], "true")
	c.Power, _ = strconv.Atoi(ini["Radio"]["Power"])
	c.RFLevel = 100 // m17-gateway's default
	if v, err := strconv.Atoi(ini["Modem"]["RFLevel"]); err == nil && v >= 0 && v <= 100 {
		c.RFLevel = v
	}
	c.Reflector = ini["Reflector"]["Name"]
	c.Module = ini["Reflector"]["Module"]

	// Ask the helper for what only root may read; without it, keep defaults
	if out, err := a.helper(ctx, "", "wifi-configured"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "country":
				c.WiFiCountry = v
			case "ssid":
				c.WiFiSSIDs = append(c.WiFiSSIDs, v)
			}
		}
	}
	// Older system versions' helpers don't have ssh-status: unknown
	c.SSHKeys = -1
	if out, err := a.helper(ctx, "", "ssh-status"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "password_logins":
				c.SSHPasswordLogins = v == "true"
			case "keys":
				if n, err := strconv.Atoi(v); err == nil {
					c.SSHKeys = n
				}
			}
		}
	}
	if out, err := a.helper(ctx, "", "update-policy"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "AUTO_UPDATE":
				c.AutoUpdate = v
			case "UPDATE_HOUR":
				if h, err := strconv.Atoi(v); err == nil {
					c.UpdateHour = h
				}
			}
		}
	}
	return c, nil
}

// online reports whether some interface has a global IPv4 address other
// than the setup access point's.
func (a *Alpine) online() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !ipn.IP.IsGlobalUnicast() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if ipn.IP.String() == a.SetupAddress {
				continue
			}
			return true
		}
	}
	return false
}

// ── WiFi ──────────────────────────────────────────────────────────────

// parseScan reads the helper's wifi-scan format: signal, MHz, flags, SSID,
// tab-separated.
func parseScan(out string) []Network {
	var nets []Network
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 {
			continue
		}
		sig, err1 := strconv.Atoi(f[0])
		freq, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		nets = append(nets, Network{
			SSID:      f[3],
			Signal:    sig,
			Frequency: freq,
			Secured:   strings.Contains(f[2], "WPA") || strings.Contains(f[2], "RSN") || strings.Contains(f[2], "WEP") || strings.Contains(f[2], "SAE"),
		})
	}
	return nets
}

func (a *Alpine) ScanWiFi(ctx context.Context) ([]Network, error) {
	out, err := a.helper(ctx, "", "wifi-scan")
	if err == nil {
		return parseScan(out), nil
	}
	// While the setup access point is up: the list saved when it started
	if b, cerr := os.ReadFile(a.ScanCache); cerr == nil {
		return parseScan(string(b)), nil
	}
	return nil, err
}

// ── The HAT ───────────────────────────────────────────────────────────

func (a *Alpine) DetectHAT(ctx context.Context) (HAT, error) {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	port := "/dev/ttyAMA0"
	if ini, err := readINI(a.GatewayINI); err == nil {
		if p := ini["Modem"]["Port"]; p != "" {
			port = p
		}
		if strings.EqualFold(ini["Modem"]["Type"], "sx1255") {
			// The SX1255 HAT has no serial interface to ask
			return HAT{}, nil
		}
	}
	// The gateway holds the port: stop it while probing, if it's running
	if _, err := a.helper(ctx, "", "gateway", "status"); err == nil {
		if _, err := a.helper(ctx, "", "gateway", "stop"); err != nil {
			return HAT{}, err
		}
		defer func() {
			// Not ctx: the gateway must restart even if the request is gone
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = a.helper(c, "", "gateway", "start")
		}()
	}
	r, err := a.Probe(port)
	if err != nil {
		return HAT{}, fmt.Errorf("probing %s: %w", port, err)
	}
	return HAT{Type: r.Type, Ident: r.Ident, Speed: r.Speed}, nil
}

// ── Firmware ──────────────────────────────────────────────────────────

func (a *Alpine) FirmwareImages(ctx context.Context) ([]firmware.Image, error) {
	return firmware.Scan(a.FirmwareDir)
}

func (a *Alpine) FlashFirmware(ctx context.Context, name string) error {
	_, err := a.helper(ctx, "", "firmware-flash", name)
	if err != nil && strings.Contains(err.Error(), "unknown command") {
		// An admin helper from before firmware flashing
		return ErrFlashUnsupported
	}
	return err
}

func (a *Alpine) FlashStatus(ctx context.Context) (FlashStatus, error) {
	out, err := a.helper(ctx, "", "firmware-status")
	if err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			return FlashStatus{}, nil
		}
		return FlashStatus{}, err
	}
	return parseFlashStatus(out), nil
}

// parseFlashStatus reads the helper's firmware-status: key=value lines,
// then "log: " lines.
func parseFlashStatus(out string) FlashStatus {
	var s FlashStatus
	for _, line := range strings.Split(out, "\n") {
		if l, ok := strings.CutPrefix(line, "log: "); ok {
			s.Log = append(s.Log, l)
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "state":
			s.State = v
		case "file":
			s.File = v
		case "started":
			s.Started, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return s
}

// ── Reflectors and time zones ─────────────────────────────────────────

func (a *Alpine) Reflectors(ctx context.Context) ([]Reflector, error) {
	byName := map[string]Reflector{}
	read := 0
	for _, path := range a.HostFiles {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		read++
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] == '#' {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			port, err := strconv.Atoi(fields[2])
			if err != nil {
				continue
			}
			byName[fields[0]] = Reflector{Name: fields[0], Server: fields[1], Port: port}
		}
		f.Close()
	}
	if read == 0 {
		return nil, errors.New("no reflector host file could be read")
	}
	out := make([]Reflector, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	return out, nil
}

func (a *Alpine) TimeZones(ctx context.Context) ([]string, error) {
	f, err := os.Open(a.ZoneTab)
	if err != nil {
		return nil, fmt.Errorf("reading the time zone list: %w", err)
	}
	defer f.Close()
	zones := []string{"UTC"}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if f := strings.Split(line, "\t"); len(f) >= 3 {
			zones = append(zones, f[2])
		}
	}
	slices.Sort(zones)
	return slices.Compact(zones), sc.Err()
}

// ── The admin password ────────────────────────────────────────────────

var hashRE = regexp.MustCompile(`'admin_password_hash'\s*=>\s*'([^']*)'`)

// passwordHash reads the admin password's hash from rpi-dashboard's
// config.php ("" if none).
func (a *Alpine) passwordHash() (string, error) {
	b, err := os.ReadFile(a.DashboardConfig)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	m := hashRE.FindSubmatch(b)
	if m == nil {
		return "", nil
	}
	return string(m[1]), nil
}

func (a *Alpine) AdminPasswordSet() (bool, error) {
	h, err := a.passwordHash()
	return h != "", err
}

func (a *Alpine) CheckAdminPassword(password string) (bool, error) {
	h, err := a.passwordHash()
	if err != nil || h == "" {
		return false, err
	}
	// PHP's password_hash writes $2y$, the same algorithm as $2a$/$2b$
	return bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) == nil, nil
}

func (a *Alpine) SetAdminPassword(ctx context.Context, password string) error {
	res, err := a.Apply(ctx, []Setting{{Key: "dashboard_password", Value: password}})
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "dashboard") {
			return errors.New(w)
		}
	}
	return nil
}

// ── Applying settings ─────────────────────────────────────────────────

func (a *Alpine) Apply(ctx context.Context, settings []Setting) (Result, error) {
	var in strings.Builder
	for _, s := range settings {
		if strings.ContainsAny(s.Key+s.Value, "\n\r") || strings.Contains(s.Key, "=") {
			return Result{}, fmt.Errorf("setting %q can't contain a line break", s.Key)
		}
		in.WriteString(s.Key + "=" + s.Value + "\n")
	}
	out, err := a.helper(ctx, in.String(), "apply")
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, ": ")
		switch k {
		case "warning":
			res.Warnings = append(res.Warnings, v)
		case "reboot":
			res.RebootNeeded = v == "true"
		case "network":
			res.NetworkChanged = v == "true"
		}
	}
	return res, nil
}

var _ Platform = (*Alpine)(nil)
