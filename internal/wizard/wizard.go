// Package wizard defines the setup wizard's steps and keeps its progress
// on disk, so a reboot or the switch from the setup access point to WiFi
// doesn't lose what the user has entered.
package wizard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/settings"
)

// Step is one page of the wizard.
type Step struct {
	ID    string // used in URLs: /setup/<ID>
	Title string
}

// Steps are the wizard's pages, in order. The admin password comes first:
// nothing else may be changed until it's set.
var Steps = []Step{
	{"password", "Admin password"},
	{"network", "Network"},
	{"station", "Station"},
	{"hat", "Radio HAT"},
	{"firmware", "Firmware"},
	{"radio", "Frequency"},
	{"reflector", "Reflector"},
	{"updates", "Updates"},
	{"ssh", "SSH"},
	{"review", "Review"},
}

// Index returns the position of step id in Steps, or -1.
func Index(id string) int {
	return slices.IndexFunc(Steps, func(s Step) bool { return s.ID == id })
}

// Next returns the step after id ("" after the last).
func Next(id string) string {
	i := Index(id)
	if i < 0 || i+1 >= len(Steps) {
		return ""
	}
	return Steps[i+1].ID
}

// Prev returns the step before id ("" before the first).
func Prev(id string) string {
	i := Index(id)
	if i <= 0 {
		return ""
	}
	return Steps[i-1].ID
}

// State is the wizard's progress.
type State struct {
	Draft settings.Draft `json:"draft"`
	// Done lists the steps whose form has been accepted.
	Done []string `json:"done"`
	// Started is false until the draft has been filled from the
	// hotspot's current settings.
	Started bool `json:"started"`
	// DetectedHAT is what the HAT step found, so its serial speed can be
	// used if the user keeps that HAT type.
	DetectedHAT platform.HAT `json:"detected_hat"`
	// FlashSeen is the start time of the last firmware flash whose end
	// this wizard has seen (and asked the HAT again what it runs).
	FlashSeen int64 `json:"flash_seen,omitempty"`
	// Steps are the IDs of the wizard's steps when the state was saved,
	// and Saved when: progress from another version of the wizard, or
	// from a run left unfinished long ago, isn't resumed (see Load).
	Steps []string  `json:"steps,omitempty"`
	Saved time.Time `json:"saved,omitzero"`
}

// IsDone reports whether step id has been completed.
func (s *State) IsDone(id string) bool { return slices.Contains(s.Done, id) }

// MarkDone records step id as completed.
func (s *State) MarkDone(id string) {
	if !s.IsDone(id) {
		s.Done = append(s.Done, id)
	}
}

// FirstOpen returns the first step not yet completed.
func (s *State) FirstOpen() string {
	for _, st := range Steps {
		if !s.IsDone(st.ID) {
			return st.ID
		}
	}
	return Steps[len(Steps)-1].ID
}

// Reachable reports whether step id may be shown: every step before it is
// done.
func (s *State) Reachable(id string) bool {
	i := Index(id)
	if i < 0 {
		return false
	}
	for _, st := range Steps[:i] {
		if !s.IsDone(st.ID) {
			return false
		}
	}
	return true
}

// Store keeps the State in a file. The file may hold a WiFi password until
// the settings are applied, so it is readable by its owner only.
type Store struct {
	mu   sync.Mutex
	path string
	// MaxAge is how long unfinished progress is kept.
	MaxAge time.Duration
	// Now is the clock (tests replace it).
	Now func() time.Time
}

// NewStore keeps the state in dir/wizard.json.
func NewStore(dir string) *Store {
	return &Store{path: filepath.Join(dir, "wizard.json"), MaxAge: 24 * time.Hour, Now: time.Now}
}

func stepIDs() []string {
	ids := make([]string, len(Steps))
	for i, s := range Steps {
		ids[i] = s.ID
	}
	return ids
}

// stale reports whether saved progress shouldn't be resumed: it was made
// with other steps (a new version of the wizard would otherwise open at
// its new step, with a draft the user filled in for the old one), or it
// was left unfinished more than MaxAge ago, so its draft may no longer
// match the hotspot's settings. The state lives on /data, so it outlasts
// system updates. A save time in the future (the clock was set back) is
// not stale.
func (st *Store) stale(s State) bool {
	if !slices.Equal(s.Steps, stepIDs()) || s.Saved.IsZero() {
		return true
	}
	return st.Now().Sub(s.Saved) > st.MaxAge
}

// Load returns the saved state, or an empty one.
func (st *Store) Load() (State, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var s State
	b, err := os.ReadFile(st.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		// A damaged file: start again rather than locking the user out
		return State{}, nil
	}
	if st.stale(s) {
		return State{}, nil
	}
	return s, nil
}

// Save writes the state atomically.
func (st *Store) Save(s State) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s.Steps = stepIDs()
	s.Saved = st.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

// Clear removes the saved state (after the settings are applied, so the
// WiFi password doesn't stay on disk).
func (st *Store) Clear() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	err := os.Remove(st.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
