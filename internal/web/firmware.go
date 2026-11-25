package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jancona/m17-web/internal/auth"
	"github.com/jancona/m17-web/internal/firmware"
	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/settings"
	"github.com/jancona/m17-web/internal/wizard"
)

// firmwareInfo is what the firmware step shows.
type firmwareInfo struct {
	// Applicable is false for a HAT without firmware to flash (SX1255).
	Applicable bool
	// Ident is what the HAT reported at the HAT step (or after the last
	// flash); "" if it didn't answer.
	Ident  string
	Report firmware.Report
	// Matches are the files for the HAT's board; Others the rest of the
	// files for its type.
	Matches []firmware.Image
	Others  []firmware.Image
	// Selected is the file chosen in the form.
	Selected string
	Status   platform.FlashStatus
	// Flashed is set when Status is the end of a flash started from here.
	Flashed bool
	// Unsupported: the system can't flash from the wizard.
	Unsupported bool
	// IdentFrom says where Ident came from, on the admin page ("the
	// gateway, at 10:00"); "" in the wizard
	IdentFrom string
	// OtherType is the HAT type the gateway reported when it isn't the
	// configured one (admin page)
	OtherType string
}

// firmwareFor builds what the firmware form shows for a HAT of type
// hatType whose firmware describes itself as ident ("" if unknown), with
// the last flash's status.
func (s *Server) firmwareFor(ctx context.Context, hatType, ident string, status platform.FlashStatus) (*firmwareInfo, error) {
	fw := &firmwareInfo{Status: status}
	families := firmware.Families(hatType)
	if len(families) == 0 {
		return fw, nil
	}
	fw.Applicable = true
	fw.Ident = ident
	imgs, err := s.plat.FirmwareImages(ctx)
	if err != nil {
		return nil, err
	}
	fw.Report = firmware.Check(fw.Ident, families, imgs)
	fw.Matches = fw.Report.Matches
	for _, img := range firmware.For(families, imgs) {
		if !slices.Contains(fw.Matches, img) {
			fw.Others = append(fw.Others, img)
		}
	}
	fw.Selected = fw.Report.Recommended
	return fw, nil
}

// flashStatus is the last flash's status (empty, logged, if it can't be
// read).
func (s *Server) flashStatus(ctx context.Context) platform.FlashStatus {
	status, err := s.plat.FlashStatus(ctx)
	if err != nil {
		s.log.Warn("firmware status", "err", err)
	}
	return status
}

// firmwareInfo builds the firmware step's data. When a flash has finished
// since the page was last shown, the HAT is asked again what it runs.
func (s *Server) firmwareInfo(ctx context.Context, st *wizard.State) (*firmwareInfo, error) {
	if len(firmware.Families(st.Draft.HATType)) == 0 {
		return &firmwareInfo{}, nil
	}
	status := s.flashStatus(ctx)
	if !status.Running() && status.State != "" && status.Started != st.FlashSeen {
		hat, err := s.plat.DetectHAT(ctx)
		if err != nil {
			s.log.Warn("HAT detection after flashing failed", "err", err)
		}
		st.DetectedHAT = hat
		st.Draft.SetDetected(hat) // the new firmware may be another kind
		st.FlashSeen = status.Started
		if err := s.store.Save(*st); err != nil {
			return nil, err
		}
	}
	ident := ""
	if st.DetectedHAT.Type == st.Draft.HATType {
		ident = st.DetectedHAT.Ident
	}
	fw, err := s.firmwareFor(ctx, st.Draft.HATType, ident, status)
	if err != nil {
		return nil, err
	}
	fw.Flashed = !status.Running() && status.State != "" && status.Started == st.FlashSeen
	return fw, nil
}

// startFlash checks the chosen file against the form's lists and starts
// writing it. It returns the error to show on the form ("" if started).
func (s *Server) startFlash(ctx context.Context, fw *firmwareInfo, name string) string {
	known := slices.ContainsFunc(append(slices.Clone(fw.Matches), fw.Others...), func(i firmware.Image) bool { return i.Name == name })
	switch {
	case fw.Status.Running():
		return "A flash is already running."
	case name == "":
		return "Choose the firmware file for your board."
	case !known:
		return "That isn't one of the firmware files."
	}
	if err := s.plat.FlashFirmware(ctx, name); err != nil {
		s.log.Warn("flashing firmware", "file", name, "err", err)
		if errors.Is(err, platform.ErrFlashUnsupported) {
			fw.Unsupported = true
		}
		return "The flash couldn't start: " + err.Error()
	}
	s.log.Info("flashing firmware", "file", name)
	return ""
}

// submitFirmware handles the firmware step's form. It returns true when
// it has answered the request; false to go on to the next step.
func (s *Server) submitFirmware(w http.ResponseWriter, r *http.Request, sess *auth.Session, st *wizard.State) bool {
	ctx := r.Context()
	fw, err := s.firmwareInfo(ctx, st)
	if err != nil {
		s.fail(w, r, err)
		return true
	}
	if !fw.Applicable {
		return false
	}
	errs := map[string]string{}
	switch r.PostFormValue("action") {
	case "flash":
		name := r.PostFormValue("firmware")
		msg := s.startFlash(ctx, fw, name)
		if msg == "" {
			http.Redirect(w, r, "/setup/firmware", http.StatusSeeOther)
			return true
		}
		errs["firmware"] = msg
		fw.Selected = name
	default:
		if !fw.Status.Running() {
			return false
		}
		errs["firmware"] = "Wait for the flash to finish."
	}
	p, err := s.newPage(ctx, sess, st, "firmware")
	if err != nil {
		s.fail(w, r, err)
		return true
	}
	fw.Status = p.Firmware.Status
	p.Firmware = fw
	p.Errors = errs
	s.render(w, r, http.StatusUnprocessableEntity, "firmware", p)
	return true
}

// hatIdent is what the HAT's firmware reports, for the admin pages: a
// detection from the Radio or Firmware page, or else the gateway's report
// when it last started (which needs no probe). Either counts only if it's
// for this HAT type and newer than the last flash: m17os-firmware
// restarts the gateway afterwards, so a new report follows a flash.
func (s *Server) hatIdent(ctx context.Context, hatType string, status platform.FlashStatus) (ident, from string) {
	flashed := time.Unix(status.Started, 0)
	s.mu.Lock()
	det := s.detected
	s.mu.Unlock()
	if det != nil && time.Since(det.At) < detectionShown && det.HAT.Type == hatType && det.At.After(flashed) {
		return det.HAT.Ident, "asked at " + det.At.Format("15:04")
	}
	si, err := s.plat.SystemInfo(ctx)
	if err != nil || si.ModemIdent == "" || si.Modem != hatType {
		return "", ""
	}
	if status.Started > 0 && si.ModemAt.Before(flashed) {
		return "", "" // from before the last flash
	}
	return si.ModemIdent, "the gateway's report when it started, " + si.ModemAt.Local().Format("Jan 2 15:04")
}

// adminFirmware builds the admin Firmware page's form data.
func (s *Server) adminFirmware(ctx context.Context, d settings.Draft) (*firmwareInfo, error) {
	status := s.flashStatus(ctx)
	ident, from := s.hatIdent(ctx, d.HATType, status)
	fw, err := s.firmwareFor(ctx, d.HATType, ident, status)
	if err != nil {
		return nil, err
	}
	fw.IdentFrom = from
	if ident == "" {
		if si, err := s.plat.SystemInfo(ctx); err == nil && si.Modem != "" && si.Modem != d.HATType {
			fw.OtherType = si.Modem
		}
	}
	// The last flash's result stays on the page for a day
	fw.Flashed = !status.Running() && status.State != "" && time.Since(time.Unix(status.Started, 0)) < 24*time.Hour
	return fw, nil
}
