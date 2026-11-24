// Package settings holds the setup wizard's answers (the Draft), checks
// them, and turns them into the m17-config.txt keys that m17os-config
// applies.
package settings

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jancona/m17-web/internal/platform"
)

// Draft is everything the wizard has collected so far.
type Draft struct {
	// Network
	UseWiFi      bool   `json:"use_wifi"`
	WiFiSSID     string `json:"wifi_ssid"`
	WiFiPassword string `json:"wifi_password"`
	WiFiCountry  string `json:"wifi_country"`
	// SavedSSIDs are the networks the hotspot already has settings for.
	// Their passwords can't be read back, so choosing one with the
	// password left empty keeps its saved settings (KeepWiFi) rather than
	// rewriting it as an open network.
	SavedSSIDs []string `json:"saved_ssids"`
	KeepWiFi   bool     `json:"keep_wifi"`

	// Station
	Callsign string `json:"callsign"` // base callsign, e.g. N1ADJ
	Suffix   string `json:"suffix"`   // optional single character, e.g. H
	Hostname string `json:"hostname"`
	TimeZone string `json:"timezone"`

	// Radio
	HATType     string  `json:"hat_type"`
	ModemSpeed  int     `json:"modem_speed"` // detected serial speed; 0 for the HAT type's default
	RXFrequency float64 `json:"rx_mhz"`
	TXFrequency float64 `json:"tx_mhz"`
	Duplex      bool    `json:"duplex"`
	Power       int     `json:"power"`
	// RFLevel is the MMDVM_HS transmit power, 1-100 percent; 0 in a
	// draft saved before it existed (then left as it is)
	RFLevel int `json:"rf_level"`
	// RFLevelWas is the hotspot's RFLevel when the wizard started. Only a
	// change is applied, so images whose m17os-config predates rf_level
	// don't warn about it on every run.
	RFLevelWas int `json:"rf_level_was"`
	// RepeaterBoard is true when the HAT step found MMDVM repeater
	// firmware, which takes its power from the attached radio
	RepeaterBoard bool `json:"repeater_board"`

	// Reflector ("" for RF only)
	Reflector string `json:"reflector"`
	Module    string `json:"module"`

	// Updates
	AutoUpdate string `json:"auto_update"`
	UpdateHour int    `json:"update_hour"`

	// SSH. SSHKey is a key to add (keys already there stay); UserPassword
	// a new password for the m17 user ("" keeps it). Like the WiFi
	// password, UserPassword is dropped once the settings are applied.
	SSHKey            string `json:"ssh_key"`
	SSHPasswordLogins bool   `json:"ssh_password_logins"`
	UserPassword      string `json:"user_password"`
	// SSHPasswordWas is whether password logins were already allowed
	SSHPasswordWas bool `json:"ssh_password_was"`
	SSHKeys        int  `json:"ssh_keys"` // keys already there; -1 if unknown
}

// FromCurrent starts a draft from the hotspot's current settings.
func FromCurrent(c platform.Current) Draft {
	base, suffix, _ := strings.Cut(c.Callsign, " ")
	d := Draft{
		UseWiFi:     !c.Online || len(c.WiFiSSIDs) > 0,
		WiFiCountry: c.WiFiCountry,
		SavedSSIDs:  slices.Clone(c.WiFiSSIDs),
		Callsign:    base,
		Suffix:      suffix,
		Hostname:    c.Hostname,
		TimeZone:    c.TimeZone,
		HATType:     c.HATType,
		RXFrequency: float64(c.RXFrequency) / 1e6,
		TXFrequency: float64(c.TXFrequency) / 1e6,
		Duplex:      c.Duplex,
		Power:       c.Power,
		RFLevel:     c.RFLevel,
		RFLevelWas:  c.RFLevel,
		Reflector:   c.Reflector,
		Module:      c.Module,
		AutoUpdate:  c.AutoUpdate,
		UpdateHour:  c.UpdateHour,

		SSHPasswordLogins: c.SSHPasswordLogins,
		SSHPasswordWas:    c.SSHPasswordLogins,
		SSHKeys:           c.SSHKeys,
	}
	if len(c.WiFiSSIDs) > 0 {
		d.WiFiSSID = c.WiFiSSIDs[0]
		d.KeepWiFi = true
	}
	if d.WiFiCountry == "" {
		d.WiFiCountry = "US"
	}
	if d.AutoUpdate == "" {
		d.AutoUpdate = "auto"
	}
	return d
}

// Problems are a form's errors (which stop the step) and warnings (which
// are shown but don't), keyed by field name.
type Problems struct {
	Errors   map[string]string
	Warnings map[string]string
}

func newProblems() Problems {
	return Problems{Errors: map[string]string{}, Warnings: map[string]string{}}
}

// OK reports whether there are no errors.
func (p Problems) OK() bool { return len(p.Errors) == 0 }

// MinPasswordLength is the shortest admin password rpi-dashboard accepts.
const MinPasswordLength = 8

// CheckAdminPassword checks a new admin password and its confirmation.
func CheckAdminPassword(password, confirm string) Problems {
	p := newProblems()
	switch {
	case utf8.RuneCountInString(password) < MinPasswordLength:
		p.Errors["password"] = fmt.Sprintf("Use at least %d characters.", MinPasswordLength)
	case password != confirm:
		p.Errors["confirm"] = "The two passwords don't match."
	}
	return p
}

// CheckNetwork checks the network step.
func (d Draft) CheckNetwork(countries []string) Problems {
	p := newProblems()
	if !d.UseWiFi {
		return p
	}
	switch n := len(d.WiFiSSID); {
	case n == 0:
		p.Errors["wifi_ssid"] = "Choose a network, or type its name."
	case n > 32:
		p.Errors["wifi_ssid"] = "A WiFi network name is at most 32 bytes."
	}
	if d.WiFiPassword != "" {
		n := len(d.WiFiPassword)
		hexKey := n == 64 && regexp.MustCompile(`^[0-9a-fA-F]+$`).MatchString(d.WiFiPassword)
		if !hexKey && (n < 8 || n > 63) {
			p.Errors["wifi_password"] = "A WPA password is 8 to 63 characters."
		}
	}
	if !slices.Contains(countries, d.WiFiCountry) {
		p.Errors["wifi_country"] = "Choose the country the hotspot is in."
	}
	return p
}

var (
	callsignRE = regexp.MustCompile(`^[A-Z0-9/.-]+$`)
	// What m17-gateway accepts (m17.EncodeCallsign): a callsign, with an
	// optional prefix and suffix, or a 6-7 character name like W100AW.
	// Slightly stricter than the gateway about separators.
	gatewayCallsignRE = regexp.MustCompile(`^(([0-9]?[A-Z]{1,2}[0-9]{0,2}/)?[0-9]?[A-Z]{1,2}[0-9]{1,2}[A-Z]{1,4}([ /.-][A-Z0-9 /.-]*)?|[A-Z][-A-Z0-9]{5,6}( +[ A-Z])?)$`)
	hostnameRE        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// UpdateKeepWiFi decides KeepWiFi after the network step: a saved
// network with no new password keeps its saved settings.
func (d *Draft) UpdateKeepWiFi() {
	d.KeepWiFi = d.UseWiFi && d.WiFiPassword == "" && slices.Contains(d.SavedSSIDs, d.WiFiSSID)
}

// FullCallsign is the callsign as the gateway uses it: "N1ADJ" or
// "N1ADJ H".
func (d Draft) FullCallsign() string {
	if d.Suffix == "" {
		return d.Callsign
	}
	return d.Callsign + " " + d.Suffix
}

// CheckStation checks the station step. Callsign and Suffix should already
// be upper case.
func (d Draft) CheckStation(timeZones []string) Problems {
	p := newProblems()
	switch {
	case d.Callsign == "":
		p.Errors["callsign"] = "Enter your callsign."
	case !callsignRE.MatchString(d.Callsign):
		p.Errors["callsign"] = "Use only letters, digits, / - and ."
	case !gatewayCallsignRE.MatchString(d.Callsign):
		// m17-gateway wouldn't start with it
		p.Errors["callsign"] = "That doesn't look like a callsign: letters, then a digit, then letters (N1ADJ, 2E0ABC), with an optional prefix (EA/N1ADJ) or suffix (N1ADJ/P)."
	case !strings.ContainsAny(d.Callsign, "0123456789"):
		p.Warnings["callsign"] = "Callsigns usually contain a digit. Check it's right."
	}
	if d.Suffix != "" && !regexp.MustCompile(`^[A-Z0-9]$`).MatchString(d.Suffix) {
		p.Errors["suffix"] = "The suffix is a single letter or digit."
	}
	// M17 encodes up to 9 characters (base 40)
	if n := len(d.FullCallsign()); n > 9 && p.Errors["callsign"] == "" {
		p.Errors["callsign"] = fmt.Sprintf("M17 callsigns are at most 9 characters with the suffix and its space (this is %d).", n)
	}
	if !hostnameRE.MatchString(d.Hostname) {
		p.Errors["hostname"] = "Use lower-case letters, digits and hyphens (not at the start or end)."
	}
	if !slices.Contains(timeZones, d.TimeZone) {
		p.Errors["timezone"] = "Choose a time zone from the list."
	}
	return p
}

// HATTypes are the radio HATs m17-gateway supports.
var HATTypes = []string{"cc1200", "sx1255", "mmdvm"}

// CheckHAT checks the HAT step.
func (d Draft) CheckHAT() Problems {
	p := newProblems()
	if !slices.Contains(HATTypes, d.HATType) {
		p.Errors["hat_type"] = "Choose the radio HAT on your Pi."
	}
	return p
}

// band is an amateur band where M17 is commonly used. Allocations differ
// between countries, so a frequency outside them is a warning only.
type band struct {
	name     string
	low, top float64 // MHz
}

var bands = []band{
	{"2 m", 144, 148},
	{"1.25 m", 219, 225},
	{"70 cm", 420, 450},
	{"33 cm", 902, 928},
	{"23 cm", 1240, 1300},
}

func inHSBand(mhz float64) bool {
	for _, b := range hsBands {
		if mhz >= b[0] && mhz <= b[1] {
			return true
		}
	}
	return false
}

func bandOf(mhz float64) string {
	for _, b := range bands {
		if mhz >= b.low && mhz <= b.top {
			return b.name
		}
	}
	return ""
}

// What m17-gateway (0.9.5 and later) accepts for each HAT: a value
// outside these stops the gateway from starting ("Bad configuration").
const (
	CC1200MinMHz = 420.0 // the CC1200 HAT's filters
	CC1200MaxMHz = 450.0
	SX1255MinMHz = 400.0 // the SX1255's datasheet range
	SX1255MaxMHz = 510.0
	MinPower     = -16 // dBm, the CC1200 HAT's range
	MaxPower     = 14
	// DefaultPower is written for HATs that don't use the setting
	DefaultPower = 10
)

// hsBands are the bands MMDVM_HS hotspot firmware accepts; it refuses
// other frequencies. MMDVM repeater boards ignore the frequency.
var hsBands = [][2]float64{{144, 148}, {219, 225}, {420, 475}, {842, 950}}

// freqRange is the frequency range the HAT accepts, in MHz, and the
// HAT's name for messages; ok is false when the gateway doesn't check it.
func (d Draft) freqRange() (lo, hi float64, name string, ok bool) {
	switch d.HATType {
	case "cc1200":
		return CC1200MinMHz, CC1200MaxMHz, "The CC1200 HAT", true
	case "sx1255":
		return SX1255MinMHz, SX1255MaxMHz, "The SX1255 HAT", true
	}
	return 0, 0, "", false
}

// UsesPower reports whether the HAT's transmit power is set in
// m17-gateway's configuration. Only the CC1200 HAT's is: MMDVM boards
// and the SX1255 HAT ignore it.
func (d Draft) UsesPower() bool { return d.HATType == "cc1200" }

// SetDetected records what the HAT step found: a HAT answering with MMDVM
// repeater firmware ("MMDVM 20251014 ...", where hotspot boards say
// "MMDVM_HS_Hat-v1.6.1 ..." or similar) takes its power from its radio.
func (d *Draft) SetDetected(h platform.HAT) {
	d.RepeaterBoard = d.HATType == "mmdvm" && h.Type == "mmdvm" && strings.HasPrefix(h.Ident, "MMDVM ")
}

// UsesRFLevel reports whether the HAT's transmit power is set as
// [Modem] RFLevel: MMDVM hotspot boards. Repeater boards ignore it.
func (d Draft) UsesRFLevel() bool { return d.HATType == "mmdvm" && !d.RepeaterBoard }

// CheckRadio checks the radio step.
func (d Draft) CheckRadio() Problems {
	p := newProblems()
	lo, hi, hatName, hasRange := d.freqRange()
	check := func(field string, mhz float64) {
		switch {
		case math.IsNaN(mhz) || mhz <= 0:
			p.Errors[field] = "Enter the frequency in MHz, e.g. 439.500."
		case mhz < 1 || mhz > 6000: // e.g. GHz or kHz typed by mistake
			p.Errors[field] = "That doesn't look like a frequency in MHz."
		case hasRange && (mhz < lo || mhz > hi):
			p.Errors[field] = fmt.Sprintf("%s works between %g and %g MHz.", hatName, lo, hi)
		case d.HATType == "mmdvm" && !inHSBand(mhz):
			p.Warnings[field] = "MMDVM_HS hotspot boards work on 144-148, 219-225, 420-475 and 842-950 MHz, and refuse other frequencies. Repeater boards don't mind."
		case bandOf(mhz) == "":
			p.Warnings[field] = "This is outside the usual amateur bands. Check it's allowed where you are."
		}
	}
	check("rx_mhz", d.RXFrequency)
	if d.Duplex {
		check("tx_mhz", d.TXFrequency)
		if p.Errors["tx_mhz"] == "" && d.TXFrequency == d.RXFrequency {
			p.Errors["tx_mhz"] = "In duplex the receive and transmit frequencies must differ."
		}
	}
	if d.UsesPower() && (d.Power < MinPower || d.Power > MaxPower) {
		p.Errors["power"] = fmt.Sprintf("Enter a power between %d and %d dBm, the CC1200 HAT's range.", MinPower, MaxPower)
	}
	if d.UsesRFLevel() && (d.RFLevel < 1 || d.RFLevel > 100) {
		p.Errors["rf_level"] = "Enter a power between 1 and 100 percent."
	}
	return p
}

var moduleRE = regexp.MustCompile(`^[A-Z]$`)

// CheckReflector checks the reflector step. An empty Reflector means RF
// only (no reflector).
func (d Draft) CheckReflector(reflectors []platform.Reflector) Problems {
	p := newProblems()
	if d.Reflector == "" {
		return p
	}
	if !slices.ContainsFunc(reflectors, func(r platform.Reflector) bool { return r.Name == d.Reflector }) {
		p.Errors["reflector"] = "Choose a reflector from the list."
	}
	if !moduleRE.MatchString(d.Module) {
		p.Errors["module"] = "The module is a single letter, A to Z."
	}
	return p
}

// CheckUpdates checks the updates step.
func (d Draft) CheckUpdates() Problems {
	p := newProblems()
	if !slices.Contains([]string{"auto", "notify", "off"}, d.AutoUpdate) {
		p.Errors["auto_update"] = "Choose how updates are handled."
	}
	if d.UpdateHour < 0 || d.UpdateHour > 23 {
		p.Errors["update_hour"] = "Choose an hour from 0 to 23."
	}
	return p
}

// DefaultUserPassword is the m17 user's password on a new card. It's
// published, so it can't be the password for SSH password logins.
const DefaultUserPassword = "m17hotspot"

var sshKeyRE = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [^\r\n]*)?$`)

// NormalizeSSHKey trims a pasted key: surrounding space, and line breaks
// that phones and terminals insert into long lines.
func NormalizeSSHKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("\r\n", "", "\n", "", "\r", "").Replace(s)
	return s
}

// CheckSSH checks the SSH step. confirm is the new password typed again.
func (d Draft) CheckSSH(confirm string) Problems {
	p := newProblems()
	if d.SSHKey != "" {
		switch {
		case strings.HasPrefix(d.SSHKey, "-----BEGIN"):
			p.Errors["ssh_key"] = "That's a private key. Paste the public key: the contents of the .pub file."
		case !sshKeyRE.MatchString(d.SSHKey):
			p.Errors["ssh_key"] = "That doesn't look like an SSH public key. It starts with ssh-ed25519, ssh-rsa or ecdsa-sha2-, all on one line."
		}
	}
	if d.UserPassword != "" {
		switch {
		case utf8.RuneCountInString(d.UserPassword) < MinPasswordLength:
			p.Errors["user_password"] = fmt.Sprintf("Use at least %d characters.", MinPasswordLength)
		case d.UserPassword == DefaultUserPassword:
			p.Errors["user_password"] = "That's the default password, which everyone knows. Choose another."
		case d.UserPassword != confirm:
			p.Errors["user_confirm"] = "The two passwords don't match."
		}
	} else if d.SSHPasswordLogins && !d.SSHPasswordWas {
		p.Errors["user_password"] = "To allow password logins, set a new password: the default one is published."
	}
	if !d.SSHPasswordLogins && d.SSHKey == "" && d.SSHKeys == 0 {
		p.Warnings["ssh_key"] = "Without a key or password logins, you won't be able to log in over SSH. That's fine if you don't need to."
	}
	return p
}

// ParseMHz reads a frequency typed in MHz ("439.5", "439,500"). It returns
// NaN for anything that isn't a number.
func ParseMHz(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// mhz formats MHz for m17-config.txt, which accepts MHz with a decimal
// point (and converts to Hz).
func mhz(f float64) string {
	return strconv.FormatFloat(math.Round(f*1e6)/1e6, 'f', -1, 64)
}

// Settings converts the draft into m17-config.txt keys, in the order
// m17os-config should apply them. The HAT comes before the radio settings
// so its default serial speed doesn't overwrite anything else.
func (d Draft) Settings() []platform.Setting {
	var s []platform.Setting
	add := func(k, v string) { s = append(s, platform.Setting{Key: k, Value: v}) }
	if d.UseWiFi {
		add("wifi_country", d.WiFiCountry)
		// A saved network kept as it is: rewriting it without its
		// password (which can't be read back) would make it an open one
		if !d.KeepWiFi {
			add("wifi_ssid", d.WiFiSSID)
			if d.WiFiPassword != "" {
				add("wifi_password", d.WiFiPassword)
			}
		}
	}
	add("hostname", d.Hostname)
	add("timezone", d.TimeZone)
	add("callsign", d.FullCallsign())
	add("hat", d.HATType)
	if d.ModemSpeed > 0 && d.HATType != "sx1255" {
		// After hat, which sets the type's usual speed
		add("modem_speed", strconv.Itoa(d.ModemSpeed))
	}
	if d.Duplex {
		add("rx_frequency", mhz(d.RXFrequency))
		add("tx_frequency", mhz(d.TXFrequency))
		add("duplex", "true")
	} else {
		add("frequency", mhz(d.RXFrequency))
		add("duplex", "false")
	}
	power := d.Power
	if !d.UsesPower() && (power < MinPower || power > MaxPower) {
		power = DefaultPower // unused; older gateways checked it anyway
	}
	add("power", strconv.Itoa(power))
	if d.UsesRFLevel() && d.RFLevel >= 1 && d.RFLevel <= 100 && d.RFLevel != d.RFLevelWas {
		add("rf_level", strconv.Itoa(d.RFLevel))
	}
	if d.Reflector != "" {
		add("reflector", d.Reflector)
		add("module", d.Module)
	} else {
		// RF only: m17os-config clears the reflector for "none" (an empty
		// value would leave the current one in place)
		add("reflector", "none")
	}
	add("auto_update", d.AutoUpdate)
	add("update_hour", strconv.Itoa(d.UpdateHour))
	if d.UserPassword != "" {
		add("user_password", d.UserPassword)
	}
	if d.SSHKey != "" {
		add("ssh_key", d.SSHKey)
	}
	// Only when it changes: on an older system the wizard can't read the
	// current setting, and sending the default would turn it off
	if d.SSHPasswordLogins != d.SSHPasswordWas {
		add("enable_ssh_password", strconv.FormatBool(d.SSHPasswordLogins))
	}
	return s
}
