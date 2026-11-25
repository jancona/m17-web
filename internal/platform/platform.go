// Package platform is everything m17-web needs from the operating
// system: reading the hotspot's current settings, scanning for WiFi
// networks, detecting the radio HAT, and applying settings.
//
// The Alpine/OpenRC implementation (m17-hotspot-os) calls a small
// privileged helper through doas; Fake is for tests and for developing the
// UI on a workstation (m17-web -dev). Other systems (Raspberry Pi OS with
// the installer) can be added as further implementations.
package platform

import (
	"context"
	"errors"

	"github.com/jancona/m17-web/internal/firmware"
)

// Platform is the operating system as m17-web sees it.
type Platform interface {
	// Current returns the hotspot's settings as they are now, to fill in
	// the wizard's forms.
	Current(ctx context.Context) (Current, error)

	// ScanWiFi lists the WiFi networks in range. While the setup access
	// point is up the radio can't scan, so this may return the list saved
	// when the access point started.
	ScanWiFi(ctx context.Context) ([]Network, error)

	// DetectHAT asks the radio HAT what it is.
	DetectHAT(ctx context.Context) (HAT, error)

	// FirmwareImages lists the firmware files the system has for the
	// HAT's microcontroller.
	FirmwareImages(ctx context.Context) ([]firmware.Image, error)

	// FlashFirmware starts writing firmware file name (an Image's Name) to
	// the HAT. It runs in the background; FlashStatus follows it.
	FlashFirmware(ctx context.Context, name string) error

	// FlashStatus reports the last flash (State "" if none has run since
	// the system started).
	FlashStatus(ctx context.Context) (FlashStatus, error)

	// Reflectors lists the reflectors in the gateway's host files.
	Reflectors(ctx context.Context) ([]Reflector, error)

	// TimeZones lists the IANA time zone names the system knows.
	TimeZones(ctx context.Context) ([]string, error)

	// AdminPasswordSet reports whether the dashboard admin password (shared
	// by rpi-dashboard and m17-web) has been set.
	AdminPasswordSet() (bool, error)

	// CheckAdminPassword reports whether password is the admin password.
	CheckAdminPassword(password string) (bool, error)

	// SetAdminPassword sets the admin password.
	SetAdminPassword(ctx context.Context, password string) error

	// Apply applies settings, given as m17-config.txt keys and values
	// (callsign, wifi_ssid, frequency, ...). Settings that couldn't be
	// applied come back as warnings; err is for a failure to apply at all.
	Apply(ctx context.Context, settings []Setting) (Result, error)

	// System is the rest, for the admin pages.
	System
}

// Current is the hotspot's current configuration.
type Current struct {
	Callsign    string // "" if not set yet
	Hostname    string
	TimeZone    string
	WiFiCountry string
	WiFiSSIDs   []string // networks already configured
	HATType     string   // cc1200, sx1255 or mmdvm
	RXFrequency int64    // Hz
	TXFrequency int64    // Hz
	Duplex      bool
	Power       int // dBm
	RFLevel     int // MMDVM_HS transmit power, percent (100 when unset)
	Reflector   string
	Module      string
	AutoUpdate  string // auto, notify or off
	UpdateHour  int
	// SSHPasswordLogins is true when SSH accepts passwords, not only keys
	SSHPasswordLogins bool
	// SSHKeys is the number of SSH keys the m17 user has; -1 if unknown
	SSHKeys int
	// Online is true when the hotspot has a network connection (Ethernet,
	// or WiFi with an address), false while the setup access point is up.
	Online bool
}

// Network is a WiFi network in range.
type Network struct {
	SSID      string
	Signal    int  // dBm; higher (closer to 0) is stronger
	Secured   bool // needs a password
	Frequency int  // MHz; 2412-2484 is 2.4 GHz
}

// HAT is what DetectHAT found.
type HAT struct {
	Type  string // cc1200 or mmdvm; "" if nothing answered
	Ident string // what the modem reported, e.g. its firmware version
	Speed int    // the serial speed it answered at (baud)
}

// FlashStatus is the state of a firmware flash.
type FlashStatus struct {
	State   string   // "" (none), "running", "ok" or "failed"
	File    string   // the firmware file
	Started int64    // when it started, in seconds since the epoch
	Log     []string // the last lines of its output
}

// Running reports whether a flash is in progress.
func (s FlashStatus) Running() bool { return s.State == "running" }

// ErrFlashUnsupported is returned by FlashFirmware on a system too old to
// flash firmware from the wizard.
var ErrFlashUnsupported = errors.New("this system version can't flash firmware from the setup wizard; update it first")

// Reflector is an entry in the host files.
type Reflector struct {
	Name   string
	Server string
	Port   int
}

// Setting is one m17-config.txt key and its value.
type Setting struct {
	Key   string
	Value string
}

// Result is what Apply did.
type Result struct {
	// Warnings are settings that weren't applied, as m17os-config reports
	// them ("callsign 'X' is not valid", ...).
	Warnings []string
	// RebootNeeded is set when a change only takes effect after a reboot
	// (the SX1255 HAT's overlay).
	RebootNeeded bool
	// NetworkChanged is set when the WiFi settings changed, so the
	// hotspot will reconnect, possibly leaving the setup access point.
	NetworkChanged bool
}
