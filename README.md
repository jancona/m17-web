# m17-web

The web UI for [m17-hotspot-os](https://github.com/jancona/m17-hotspot-os). Today it's the setup wizard: admin password, WiFi, callsign, radio HAT and its firmware, frequency, reflector, automatic updates and SSH, in ten short steps that work on a phone. It's growing into the hotspot's whole UI: system and admin pages next, then the dashboard, replacing rpi-dashboard. It was called m17-admin until November 2026.

> **Status: in development.** The wizard works against a simulated hotspot (`-dev`) and, on m17-hotspot-os, against the real one (`internal/platform/alpine.go`), including the setup access point.

## Running it

```bash
go run ./cmd/m17-web -dev
```

Then open http://127.0.0.1:8090/setup/. `-dev` simulates an unconfigured hotspot with an MMDVM_HS_Dual_Hat and a few WiFi networks, and keeps the wizard's state in a temporary directory that's removed on exit.

## How it fits together

- **Two ways through the same settings.** The settings come in sections (callsign, reflector, HAT, frequency, WiFi, host name, time zone, updates, SSH, admin password), each with its fields (`templates/sections.html`), its validation and its `m17-config.txt` keys (`internal/web/sections.go`). The wizard shows a few sections per step and applies everything at the end. The admin pages (`/admin/`: Station, Radio, Network, Updates, Security, System) show each section as its own form, applied when it's saved.
- **Setup, then admin.** Applying the wizard writes `setup-done` in the state directory. After that (or on a hotspot that already has a callsign), logging in and `/setup/` open the admin pages. System → Run setup again goes through the wizard again from the current settings, with every step open.
- **Live activity over MQTT.** With `-mqtt host:port` (default `127.0.0.1:1883`) m17-web subscribes to m17-gateway's status (`-mqtt-name`, default `m17-gateway`), keeps the last heard on `/data` (`lastheard.json`), and shows it at `/setup/activity` (no login, like the dashboard; JSON at `/setup/api/activity`). The broker is optional: `-mqtt ""` turns it off, and the page then says live status is off. It reconnects by itself if the broker is down.
- **Behind nginx.** m17-web listens on `127.0.0.1:8090`; nginx sends it `/setup/` and `/admin/`. While the setup access point is up, nginx sends it every host name, and m17-web answers the phones' captive-portal checks (`/generate_204`, `/hotspot-detect.html`, `/connecttest.txt`, ...) with a redirect to the wizard.
- **One admin password.** It's the password for rpi-dashboard's admin pages (a bcrypt hash in its `config.php`). Until it is set, only the first step is open; after that, a login is required.
- **Settings are applied by m17os-config.** The wizard produces the same keys as `m17-config.txt` (`wifi_ssid`, `callsign`, `frequency`, ...), so there is one implementation of applying them, and its warnings come back to the done page.
- **Progress is kept on disk** (`/var/lib/m17-web/wizard.json`, mode 0600), so a reboot or the switch from the setup access point to WiFi doesn't lose it. It's deleted once the settings are applied, since it may hold the WiFi password.
- **On m17-hotspot-os** m17-web runs as its own user. It reads the gateway's INI file, the host files, the time zone list and the dashboard's password hash directly, and does everything that needs root (applying settings, WiFi scans, stopping the gateway to probe the HAT, flashing firmware) through `/usr/local/libexec/m17os/admin-helper`, which `doas` lets it run.
- **The OS-specific parts** (applying settings, WiFi, HAT detection) are behind `internal/platform.Platform`, so a Raspberry Pi OS implementation can be added for the installer.

| Package | |
|---|---|
| `cmd/m17-web` | Flags and the HTTP server |
| `internal/web` | Routes, handlers, the sections, templates, CSS and the little JavaScript (every page works without it) |
| `internal/wizard` | The steps, their order, and the saved progress |
| `internal/settings` | The draft, its validation, and the m17-config.txt keys it becomes |
| `internal/auth` | Sessions (new ID at login), CSRF tokens, slowing down wrong passwords |
| `internal/platform` | The interface to the OS: `Alpine` for m17-hotspot-os, and the fake used by `-dev` and the tests |
| `internal/hat` | Identifies a CC1200 HAT or MMDVM board on the serial port, and the speed it answers at (a port of the installer's probe) |
| `internal/firmware` | Reads the description built into each firmware file and picks the file for the HAT's board, and whether it's newer than what the HAT runs |

No web framework: the standard library (`net/http` routing, `html/template`, `embed`, `log/slog`) plus `golang.org/x/crypto/bcrypt`.

## Tests

```bash
go test -race ./...
```

The web tests run whole wizards through the HTTP handlers: the order of the steps, login and logout, CSRF rejection, a new session ID at login, validation errors that keep what was typed, warnings that don't block, the applied settings, and that the WiFi password isn't kept or shown afterwards.

## License

Apache-2.0. See [LICENSE](LICENSE).
