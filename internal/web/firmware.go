package web

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/jancona/m17-web/internal/auth"
	"github.com/jancona/m17-web/internal/firmware"
	"github.com/jancona/m17-web/internal/platform"
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
}

// firmwareInfo builds the firmware step's data. When a flash has finished
// since the page was last shown, the HAT is asked again what it runs.
func (s *Server) firmwareInfo(ctx context.Context, st *wizard.State) (*firmwareInfo, error) {
	fw := &firmwareInfo{}
	families := firmware.Families(st.Draft.HATType)
	if len(families) == 0 {
		return fw, nil
	}
	fw.Applicable = true

	status, err := s.plat.FlashStatus(ctx)
	if err != nil {
		s.log.Warn("firmware status", "err", err)
	}
	fw.Status = status
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
	fw.Flashed = !status.Running() && status.State != "" && status.Started == st.FlashSeen

	if st.DetectedHAT.Type == st.Draft.HATType {
		fw.Ident = st.DetectedHAT.Ident
	}
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
		known := slices.ContainsFunc(append(slices.Clone(fw.Matches), fw.Others...), func(i firmware.Image) bool { return i.Name == name })
		switch {
		case fw.Status.Running():
			errs["firmware"] = "A flash is already running."
		case name == "":
			errs["firmware"] = "Choose the firmware file for your board."
		case !known:
			errs["firmware"] = "That isn't one of the firmware files."
		default:
			err := s.plat.FlashFirmware(ctx, name)
			if err == nil {
				s.log.Info("flashing firmware", "file", name)
				http.Redirect(w, r, "/setup/firmware", http.StatusSeeOther)
				return true
			}
			s.log.Warn("flashing firmware", "file", name, "err", err)
			if errors.Is(err, platform.ErrFlashUnsupported) {
				fw.Unsupported = true
			}
			errs["firmware"] = "The flash couldn't start: " + err.Error()
		}
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
