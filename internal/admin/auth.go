package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie  = "vaultd_session"
	loginCookie    = "vaultd_login"
	csrfHeader     = "X-CSRF-Token"
	csrfField      = "csrf"
	loginAttempts  = 5
	loginWindow    = time.Minute
	loginCookieTTL = time.Hour
)

func randomToken() string {
	raw := make([]byte, 32)
	rand.Read(raw) // never fails: crypto/rand panics rather than return an error
	return base64.RawURLEncoding.EncodeToString(raw)
}

func tokensEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// session is one logged-in browser. Sessions live only in memory, so a restart
// logs everyone out.
type session struct {
	csrf         string
	expires      time.Time
	passwordHash string // a password change ends sessions from before it
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]session{}}
}

func (s *sessionStore) create(ttl time.Duration, passwordHash string, now time.Time) (string, session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, existing := range s.sessions {
		if now.After(existing.expires) {
			delete(s.sessions, id)
		}
	}
	id := randomToken()
	created := session{csrf: randomToken(), expires: now.Add(ttl), passwordHash: passwordHash}
	s.sessions[id] = created
	return id, created
}

func (s *sessionStore) lookup(id, passwordHash string, now time.Time) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.sessions[id]
	if !ok {
		return session{}, false
	}
	if now.After(found.expires) || found.passwordHash != passwordHash {
		delete(s.sessions, id)
		return session{}, false
	}
	return found, true
}

func (s *sessionStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// loginLimiter counts attempts across all clients. This is a single-user tool
// on loopback, so there is no one else to lock out and no address worth telling
// apart.
type loginLimiter struct {
	mu       sync.Mutex
	attempts []time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{} }

func (l *loginLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.attempts[:0]
	for _, at := range l.attempts {
		if now.Sub(at) < loginWindow {
			recent = append(recent, at)
		}
	}
	l.attempts = recent
	if len(recent) >= loginAttempts {
		return false
	}
	l.attempts = append(l.attempts, now)
	return true
}

// requirePasswordSet shows only the setup page until a password exists.
func (p *Portal) requirePasswordSet(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p.store.Get().Admin.PasswordHash == "" {
			p.render(w, "setup", pageData{Title: "Set a password"})
			return
		}
		next(w, r)
	}
}

type sessionKey struct{}

// requireSession lets logged-in browsers through and checks the CSRF token on
// every request that changes something.
func (p *Portal) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, ok := p.currentSession(r)
		if !ok {
			redirectToLogin(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !validCSRF(r, current.csrf) {
			http.Error(w, "invalid CSRF token; reload the page", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, current)))
	}
}

func (p *Portal) currentSession(r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	return p.sessions.lookup(cookie.Value, p.store.Get().Admin.PasswordHash, time.Now())
}

func validCSRF(r *http.Request, want string) bool {
	if tokensEqual(r.Header.Get(csrfHeader), want) {
		return true
	}
	return tokensEqual(r.PostFormValue(csrfField), want)
}

// redirectToLogin tells htmx to load the whole page. A plain redirect would
// paste the login form into whatever part of the page was refreshing.
func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", "/admin/login")
		return
	}
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// loginPage has no session yet to tie a CSRF token to, so it uses the "double
// submit" method: the token sits in a cookie and in the form. A foreign site can
// post a form but can neither read nor set the cookie.
func (p *Portal) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.currentSession(r); ok {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	p.renderLogin(w, "", http.StatusOK)
}

func (p *Portal) renderLogin(w http.ResponseWriter, message string, status int) {
	token := randomToken()
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookie,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(loginCookieTTL),
	})
	w.WriteHeader(status)
	p.render(w, "login", pageData{Title: "Log in", CSRF: token, Error: message})
}

func (p *Portal) loginSubmit(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if !p.limiter.allow(now) {
		p.renderLogin(w, "Too many attempts. Wait a minute and try again.", http.StatusTooManyRequests)
		return
	}
	cookie, err := r.Cookie(loginCookie)
	if err != nil || !tokensEqual(r.PostFormValue(csrfField), cookie.Value) {
		http.Error(w, "invalid CSRF token; reload the page", http.StatusForbidden)
		return
	}
	cfg := p.store.Get()
	err = bcrypt.CompareHashAndPassword([]byte(cfg.Admin.PasswordHash), []byte(r.PostFormValue("password")))
	if err != nil {
		p.renderLogin(w, "Wrong password.", http.StatusUnauthorized)
		return
	}
	id, created := p.sessions.create(cfg.Admin.SessionTTL, cfg.Admin.PasswordHash, now)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  created.expires,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (p *Portal) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		p.sessions.remove(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
