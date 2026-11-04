package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "hatrack_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// HashPassword returns the bcrypt hash that is stored on the server.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func newSessionToken() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// access says what a browser request may do.
type access int

const (
	noAccess   access = iota
	readAccess        // a device token cookie: look, do not touch
	fullAccess        // a logged-in session
)

func (s *Server) accessOf(r *http.Request) access {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if ok, _ := s.st.SessionValid(r.Context(), tokenHash(c.Value), s.Now()); ok {
			return fullAccess
		}
	}
	if c, err := r.Cookie(tokenCookie); err == nil {
		if _, err := s.st.DeviceByToken(r.Context(), c.Value); err == nil {
			return readAccess
		}
	}
	return noAccess
}

// sameOrigin rejects cross-site form posts. The session cookie is
// SameSite=Strict as well; this is the second lock.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// loginLimiter slows down password guessing.
type loginLimiter struct {
	mu    sync.Mutex
	fails int
	Delay func(fails int) time.Duration
}

func (l *loginLimiter) wait() {
	l.mu.Lock()
	n := l.fails
	l.mu.Unlock()
	if d := l.Delay(n); d > 0 {
		time.Sleep(d)
	}
}

func (l *loginLimiter) result(ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ok {
		l.fails = 0
	} else {
		l.fails++
	}
}

func defaultDelay(fails int) time.Duration {
	return min(time.Duration(fails)*500*time.Millisecond, 4*time.Second)
}
