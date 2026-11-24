// Package auth keeps login sessions and CSRF tokens for m17-web.
//
// m17-web has one user, the hotspot's administrator, with the same
// password as rpi-dashboard's admin pages. Sessions are kept in memory: a
// restart means logging in again, which is fine for a hotspot.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

const (
	// CookieName is the session cookie.
	CookieName = "m17web"
	// SessionLifetime is how long a session lasts without use.
	SessionLifetime = 12 * time.Hour
	// failureDelay slows down password guessing.
	failureDelay = 2 * time.Second
)

// Session is one browser's login.
type Session struct {
	ID       string
	CSRF     string
	LoggedIn bool
	expires  time.Time
}

// Sessions are the active sessions.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]*Session
	now      func() time.Time
	// Sleep is called after a failed login; tests replace it.
	Sleep func(time.Duration)
}

// NewSessions returns an empty session store.
func NewSessions() *Sessions {
	return &Sessions{
		sessions: map[string]*Session{},
		now:      time.Now,
		Sleep:    time.Sleep,
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on supported systems
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Get returns the request's session, starting a new one (and setting its
// cookie) if there is none or it has expired.
func (s *Sessions) Get(w http.ResponseWriter, r *http.Request) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if c, err := r.Cookie(CookieName); err == nil {
		if sess, ok := s.sessions[c.Value]; ok && now.Before(sess.expires) {
			sess.expires = now.Add(SessionLifetime)
			return sess
		}
	}
	// Drop expired sessions while we're here
	for id, sess := range s.sessions {
		if !now.Before(sess.expires) {
			delete(s.sessions, id)
		}
	}
	sess := &Session{ID: randomToken(), CSRF: randomToken(), expires: now.Add(SessionLifetime)}
	s.sessions[sess.ID] = sess
	setCookie(w, sess.ID)
	return sess
}

// LogIn marks the session as logged in. It gets a new ID, so an ID seen
// before login (session fixation) is useless afterwards.
func (s *Sessions) LogIn(w http.ResponseWriter, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sess.ID)
	sess.ID = randomToken()
	sess.CSRF = randomToken()
	sess.LoggedIn = true
	sess.expires = s.now().Add(SessionLifetime)
	s.sessions[sess.ID] = sess
	setCookie(w, sess.ID)
}

// LogOut ends the session.
func (s *Sessions) LogOut(w http.ResponseWriter, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sess.ID)
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// Failed slows down the caller after a wrong password.
func (s *Sessions) Failed() { s.Sleep(failureDelay) }

// CheckCSRF reports whether token is the session's CSRF token.
func (sess *Session) CheckCSRF(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRF)) == 1
}

func setCookie(w http.ResponseWriter, id string) {
	// Not Secure: the hotspot is reached over plain HTTP on the LAN or
	// the setup access point
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionLifetime / time.Second),
	})
}
