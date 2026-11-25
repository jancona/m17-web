package web

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/settings"
)

// A section is a group of settings with its own fields (the f-<ID>
// template in sections.html), validation and m17-config.txt keys. The
// wizard's steps and the admin pages are both made of sections: a step
// shows several in one form, an admin page shows each in a form of its
// own.
type section struct {
	ID    string
	Title string
	// load fetches what the section's template shows besides the draft
	// (the WiFi scan, the reflector list...). It may be nil.
	load func(s *Server, ctx context.Context, p *page) error
	// parse reads the section's fields into d and checks them.
	parse func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error)
	// keys are the settings applied for the section.
	keys func(settings.Draft) []platform.Setting
}

// sectionInput is what parse gets besides the draft.
type sectionInput struct {
	form url.Values
	// prev is the draft before this form: fields left empty on purpose
	// (a password already entered) keep its values
	prev settings.Draft
	// detected is what the last HAT detection found, if detectRan
	detected  platform.HAT
	detectRan bool
}

var sections = map[string]section{
	"wifi": {
		ID: "wifi", Title: "WiFi",
		load: func(s *Server, ctx context.Context, p *page) error {
			nets, err := s.plat.ScanWiFi(ctx)
			if err != nil {
				s.log.Warn("WiFi scan failed", "err", err)
			}
			p.Networks = strongest(nets)
			p.Countries = settings.Countries
			return nil
		},
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			f := in.form
			d.UseWiFi = f.Get("connection") != "ethernet"
			d.WiFiSSID = f.Get("wifi_ssid")
			if d.WiFiSSID == "" || d.WiFiSSID == "-other-" {
				d.WiFiSSID = strings.TrimSpace(f.Get("wifi_ssid_other"))
			}
			// An empty password field keeps the one entered before, so going
			// back to this step doesn't make the user type it again
			if pw := f.Get("wifi_password"); pw != "" || d.WiFiSSID != in.prev.WiFiSSID {
				d.WiFiPassword = pw
			}
			d.WiFiCountry = strings.ToUpper(f.Get("wifi_country"))
			d.UpdateKeepWiFi()
			return d.CheckNetwork(settings.CountryCodes()), nil
		},
		keys: settings.Draft.WiFiKeys,
	},
	"callsign": {
		ID: "callsign", Title: "Callsign",
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			d.Callsign = strings.ToUpper(strings.TrimSpace(in.form.Get("callsign")))
			d.Suffix = strings.ToUpper(strings.TrimSpace(in.form.Get("suffix")))
			return d.CheckCallsign(), nil
		},
		keys: settings.Draft.CallsignKeys,
	},
	"timezone": {
		ID: "timezone", Title: "Time zone",
		load: func(s *Server, ctx context.Context, p *page) (err error) {
			p.TimeZones, err = s.plat.TimeZones(ctx)
			return err
		},
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			d.TimeZone = in.form.Get("timezone")
			zones, err := s.plat.TimeZones(ctx)
			if err != nil {
				return settings.Problems{}, err
			}
			return d.CheckTimeZone(zones), nil
		},
		keys: settings.Draft.TimeZoneKeys,
	},
	"hostname": {
		ID: "hostname", Title: "Hotspot name",
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			d.Hostname = strings.ToLower(strings.TrimSpace(in.form.Get("hostname")))
			return d.CheckHostname(), nil
		},
		keys: settings.Draft.HostnameKeys,
	},
	"hat": {
		ID: "hat", Title: "Radio HAT",
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			d.HATType = in.form.Get("hat_type")
			// The speed the HAT answered at, if it's the type chosen; otherwise
			// the type's usual speed
			d.ModemSpeed = 0
			if in.detected.Type == d.HATType && in.detected.Speed > 0 {
				d.ModemSpeed = in.detected.Speed
			}
			if d.HATType == "cc1200" {
				d.Duplex = false // the CC1200 HAT is simplex
			}
			// Without a detection (an admin page where Detect wasn't
			// pressed), what the draft knows about the board stays
			if in.detectRan {
				d.SetDetected(in.detected)
			}
			return d.CheckHAT(), nil
		},
		keys: settings.Draft.HATKeys,
	},
	"frequency": {
		ID: "frequency", Title: "Frequency and power",
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			f := in.form
			var err error
			d.RXFrequency = settings.ParseMHz(f.Get("rx_mhz"))
			d.Duplex = f.Get("duplex") == "on" && d.HATType != "cc1200"
			d.TXFrequency = d.RXFrequency
			if d.Duplex {
				d.TXFrequency = settings.ParseMHz(f.Get("tx_mhz"))
			}
			if d.UsesPower() {
				d.Power, err = strconv.Atoi(strings.TrimSpace(f.Get("power")))
				if err != nil {
					d.Power = -999 // reported by CheckRadio
				}
			}
			if d.UsesRFLevel() {
				if v := strings.TrimSpace(f.Get("rf_level")); v != "" {
					d.RFLevel, err = strconv.Atoi(v)
					if err != nil {
						d.RFLevel = -1 // reported by CheckRadio
					}
				} else if d.RFLevel == 0 {
					d.RFLevel = 100 // a draft from before the setting: the gateway's default
				}
			}
			return d.CheckRadio(), nil
		},
		keys: settings.Draft.FrequencyKeys,
	},
	"reflector": {
		ID: "reflector", Title: "Reflector",
		load: func(s *Server, ctx context.Context, p *page) error {
			refl, err := s.plat.Reflectors(ctx)
			if err != nil {
				return err
			}
			slices.SortFunc(refl, func(a, b platform.Reflector) int { return cmp.Compare(a.Name, b.Name) })
			p.Reflectors = refl
			return nil
		},
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			d.Reflector = in.form.Get("reflector")
			d.Module = strings.ToUpper(in.form.Get("module"))
			refl, err := s.plat.Reflectors(ctx)
			if err != nil {
				return settings.Problems{}, err
			}
			return d.CheckReflector(refl), nil
		},
		keys: settings.Draft.ReflectorKeys,
	},
	"updates": {
		ID: "updates", Title: "Automatic updates",
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			var err error
			d.AutoUpdate = in.form.Get("auto_update")
			d.UpdateHour, err = strconv.Atoi(in.form.Get("update_hour"))
			if err != nil {
				d.UpdateHour = -1
			}
			return d.CheckUpdates(), nil
		},
		keys: settings.Draft.UpdatesKeys,
	},
	"ssh": {
		ID: "ssh", Title: "SSH",
		parse: func(s *Server, ctx context.Context, in sectionInput, d *settings.Draft) (settings.Problems, error) {
			f := in.form
			d.SSHKey = settings.NormalizeSSHKey(f.Get("ssh_key"))
			d.SSHPasswordLogins = f.Get("ssh_password_logins") == "on"
			// An empty password field keeps one entered before
			if pw := f.Get("user_password"); pw != "" {
				d.UserPassword = pw
				probs := d.CheckSSH(f.Get("user_confirm"))
				if probs.Errors["user_password"] != "" || probs.Errors["user_confirm"] != "" {
					// A rejected password isn't kept: the form then mustn't
					// say a new one is set
					d.UserPassword = in.prev.UserPassword
				}
				return probs, nil
			}
			return d.CheckSSH(d.UserPassword), nil
		},
		keys: settings.Draft.SSHKeySettings,
	},
}

// stepSections are the sections of each wizard step that is made of
// sections (password, firmware and review have their own handling).
var stepSections = map[string][]string{
	"network":   {"wifi"},
	"station":   {"callsign", "timezone", "hostname"},
	"hat":       {"hat"},
	"radio":     {"frequency"},
	"reflector": {"reflector"},
	"updates":   {"updates"},
	"ssh":       {"ssh"},
}

// loadSections runs the load of each section in ids.
func (s *Server) loadSections(ctx context.Context, p *page, ids ...string) error {
	for _, id := range ids {
		if l := sections[id].load; l != nil {
			if err := l(s, ctx, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseSections parses the sections in ids from one form into d.
func (s *Server) parseSections(ctx context.Context, in sectionInput, d *settings.Draft, ids ...string) (settings.Problems, error) {
	var all []settings.Problems
	for _, id := range ids {
		p, err := sections[id].parse(s, ctx, in, d)
		if err != nil {
			return settings.Problems{}, err
		}
		all = append(all, p)
	}
	return settings.Merge(all...), nil
}
