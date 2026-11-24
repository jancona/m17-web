// Package hat identifies the radio HAT on the Pi's serial port: the M17
// Project's CC1200 HAT, or an MMDVM board. It is a port of the probe in
// m17-hotspot-installer.
//
// The port must be free: stop m17-gateway first.
package hat

import (
	"bytes"
	"io"
	"time"

	"go.bug.st/serial"

	"github.com/jancona/m17-web/internal/firmware"
)

// Port is the part of a serial port the probe uses.
type Port interface {
	io.ReadWriter
	Close() error
	ResetInputBuffer() error
	SetReadTimeout(time.Duration) error
}

// Opener opens path at baud bits per second.
type Opener func(path string, baud int) (Port, error)

// OpenSerial opens a real serial port: 8N1, raw.
func OpenSerial(path string, baud int) (Port, error) {
	p, err := serial.Open(path, &serial.Mode{BaudRate: baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Result is what answered.
type Result struct {
	Type  string // "cc1200" or "mmdvm"; "" if nothing answered
	Ident string // the modem's description of itself, on one line
	Speed int    // the baud rate it answered at
}

var (
	cc1200StopRX = []byte{0x08, 0x04, 0x00, 0x00} // CMD_RX_START 0: stop streaming
	cc1200Ident  = []byte{0x80, 0x03, 0x00}       // CMD_GET_IDENT
	mmdvmVersion = []byte{0xE0, 0x03, 0x00}       // GetVersion
)

// Probe asks the HAT on path what it is. It tries the CC1200 HAT at
// 460800 baud first (twice: a HAT left streaming needs the first try to
// stop it), then MMDVM's GetVersion at the two speeds MMDVM boards
// commonly use. A Result with no Type and a nil error means nothing
// answered.
func Probe(path string, open Opener) (Result, error) {
	var lastErr error
	ask := func(baud int, req []byte, wait time.Duration) []byte {
		p, err := open(path, baud)
		if err != nil {
			lastErr = err
			return nil
		}
		defer p.Close()
		time.Sleep(50 * time.Millisecond)
		_ = p.ResetInputBuffer()
		if _, err := p.Write(req); err != nil {
			lastErr = err
			return nil
		}
		return readFor(p, wait)
	}

	for range 2 {
		ask(460800, cc1200StopRX, 100*time.Millisecond)
		if t := parseCC1200(ask(460800, cc1200Ident, 500*time.Millisecond)); t != "" {
			return Result{Type: "cc1200", Ident: oneLine(t), Speed: 460800}, nil
		}
	}
	for _, baud := range []int{115200, 460800} {
		if t := parseMMDVM(ask(baud, mmdvmVersion, 500*time.Millisecond)); t != "" {
			return Result{Type: "mmdvm", Ident: oneLine(t), Speed: baud}, nil
		}
	}
	if lastErr != nil {
		return Result{}, lastErr
	}
	return Result{}, nil
}

// readFor collects what arrives within d.
func readFor(p Port, d time.Duration) []byte {
	var buf bytes.Buffer
	end := time.Now().Add(d)
	chunk := make([]byte, 4096)
	for {
		left := time.Until(end)
		if left <= 0 {
			break
		}
		if err := p.SetReadTimeout(left); err != nil {
			break
		}
		n, err := p.Read(chunk)
		buf.Write(chunk[:n])
		if n == 0 || err != nil {
			break // timed out (or the port failed)
		}
	}
	return buf.Bytes()
}

// text returns b up to its first NUL, trimmed, if it is printable text
// of at least 4 characters.
func text(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	b = bytes.TrimSpace(b)
	if len(b) < 4 {
		return ""
	}
	for _, c := range b {
		if (c < 32 || c >= 127) && c != '\n' && c != '\r' {
			return ""
		}
	}
	return string(b)
}

// parseCC1200 finds a CMD_GET_IDENT reply: 0x80, its total length (LE16),
// then the text.
func parseCC1200(buf []byte) string {
	for i := bytes.IndexByte(buf, 0x80); i >= 0; {
		if i+3 <= len(buf) {
			n := int(buf[i+1]) | int(buf[i+2])<<8
			if n >= 8 && n <= 131 && i+n <= len(buf) {
				if t := text(buf[i+3 : i+n]); t != "" {
					return t
				}
			}
		}
		j := bytes.IndexByte(buf[i+1:], 0x80)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return ""
}

// parseMMDVM finds a GetVersion reply: 0xE0, its length, 0x00, the
// protocol version, then the description (protocol 1) or capabilities
// and a UDID before it (protocol 2).
func parseMMDVM(buf []byte) string {
	for i := bytes.IndexByte(buf, 0xE0); i >= 0; {
		if i+4 <= len(buf) {
			n := int(buf[i+1])
			if buf[i+2] == 0 && n >= 8 && i+n <= len(buf) {
				frame := buf[i : i+n]
				var desc []byte
				switch frame[3] {
				case 1:
					desc = frame[4:]
				case 2:
					if len(frame) > 23 {
						desc = frame[23:]
					}
				}
				if t := text(desc); t != "" {
					return t
				}
			}
		}
		j := bytes.IndexByte(buf[i+1:], 0xE0)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return ""
}

// oneLine is firmware.OneLine: the firmware files' descriptions are put
// on one line the same way, so the two can be compared.
func oneLine(s string) string { return firmware.OneLine(s) }
