package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// System is what the admin pages need from the operating system beyond
// the wizard's settings: the hotspot's state, saved WiFi networks and SSH
// keys, OS updates, logs, and restarting.
type System interface {
	// SystemInfo describes the hardware and software, and how they're doing.
	SystemInfo(ctx context.Context) (SystemInfo, error)

	// SavedWiFi lists the saved WiFi networks, most preferred first.
	SavedWiFi(ctx context.Context) ([]SavedNetwork, error)
	// WiFiStatus is the WiFi connection now.
	WiFiStatus(ctx context.Context) (WiFiStatus, error)
	// AddWiFi saves a network (replacing one of the same name, keeping
	// the others) and reconnects. An empty password is an open network.
	AddWiFi(ctx context.Context, ssid, password string) error
	// RemoveWiFi removes a saved network and reconnects.
	RemoveWiFi(ctx context.Context, ssid string) error

	// SSHKeys lists the m17 user's public keys.
	SSHKeys(ctx context.Context) ([]SSHKey, error)
	// RemoveSSHKey removes the key whose base64 part is key.
	RemoveSSHKey(ctx context.Context, key string) error

	// OSStatus is the A/B slots and the last automatic update run.
	OSStatus(ctx context.Context) (OSStatus, error)
	// StartJob starts a background job: "os-check" or "os-install".
	StartJob(ctx context.Context, name string) error
	// JobStatus reports the last run of job name (State "" if none).
	JobStatus(ctx context.Context, name string) (JobStatus, error)
	// Rollback switches to the previous slot and reboots.
	Rollback(ctx context.Context) error

	// Log returns the last lines of log name: one of LogNames.
	Log(ctx context.Context, name string) ([]string, error)

	// Reboot restarts the hotspot, a few seconds after the call returns.
	Reboot(ctx context.Context) error
	// Shutdown powers it off, a few seconds after the call returns.
	Shutdown(ctx context.Context) error
}

// ErrUnsupported is returned by a System method on a system too old for it
// (its admin helper doesn't have the command).
var ErrUnsupported = errors.New("this system version can't do this from the web pages; update it first")

// LogNames are the logs Log can read, in the order the Logs page shows them.
var LogNames = []string{"events", "gateway", "system", "web"}

// SystemInfo describes the hotspot.
type SystemInfo struct {
	Board     string // the Pi model
	Hostname  string
	OSVersion string
	Kernel    string
	Uptime    time.Duration
	// TempC is the CPU temperature; HasTemp is false if it can't be read
	TempC   float64
	HasTemp bool
	// Memory and the /data file system, in bytes
	MemTotal, MemAvailable uint64
	DiskTotal, DiskFree    uint64
	Load                   string // the 1, 5 and 15 minute load averages
	Addresses              []string
	// Modem is what the gateway reported when it last started (its
	// "Gateway Start" event): the modem type and its description
	Modem      string
	ModemIdent string
	ModemAt    time.Time
}

// SavedNetwork is a saved WiFi network.
type SavedNetwork struct {
	SSID     string
	Priority int // higher is tried first
	Open     bool
}

// WiFiStatus is the WiFi connection.
type WiFiStatus struct {
	State   string // wpa_supplicant's: COMPLETED when connected
	SSID    string
	Signal  int // dBm, 0 if unknown
	FreqMHz int
	Address string
}

// Connected reports whether the WiFi is connected to a network.
func (w WiFiStatus) Connected() bool { return w.State == "COMPLETED" && w.SSID != "" }

// SSHKey is an authorized SSH public key.
type SSHKey struct {
	Type    string // ssh-ed25519, ssh-rsa, ...
	Key     string // the base64 part
	Comment string
}

// Fingerprint is the key's SHA256 fingerprint, as ssh-keygen -l shows it.
func (k SSHKey) Fingerprint() string {
	b, err := base64.StdEncoding.DecodeString(k.Key)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// OSStatus is the state of the A/B slots and of automatic updates.
type OSStatus struct {
	Running      string // the version running
	ActiveSlot   string
	PreviousSlot string
	SlotA, SlotB string // the version in each slot
	Pending      bool   // the active slot hasn't passed its health check yet
	BootTries    int
	LastUpdate   map[string]string // the last automatic update run (RESULT, TIME, ...)
	// PreviousVersion is the version a rollback would go back to ("" if
	// there's nothing to roll back to)
	PreviousVersion string
}

// JobStatus is the state of a background job.
type JobStatus struct {
	State   string // "", "running", "ok" or "failed"
	Exit    int
	Started int64 // seconds since the epoch
	Log     []string
}

// Running reports whether the job is still running.
func (j JobStatus) Running() bool { return j.State == "running" }

// parseKV reads key=value lines, then "log: " lines.
func parseKV(out string) (map[string]string, []string) {
	kv := map[string]string{}
	var log []string
	for _, line := range strings.Split(out, "\n") {
		if l, ok := strings.CutPrefix(line, "log: "); ok {
			log = append(log, l)
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[k] = v
		}
	}
	return kv, log
}
