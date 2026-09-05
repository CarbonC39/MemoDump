// Package httpx holds the shared HTTP helpers and the session-based auth for
// the note/image/sync API. It depends only on appstate for the runtime's
// no-auth / credential configuration.
package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"memodump/internal/appstate"
)

// WriteErr writes a legacy-shaped error body: {"error":"message"}.
func WriteErr(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// WriteJSON writes a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// ---- session auth ----

type session struct {
	token   string
	expires time.Time
}

var (
	sessions    = make(map[string]*session)
	sessionMu   sync.RWMutex
	sessionTTL  = 30 * 24 * time.Hour // 30 days
	SessionFile string
)

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LoadSessions reads persisted sessions from disk, discarding expired ones.
func LoadSessions() {
	if SessionFile == "" {
		return
	}
	data, err := os.ReadFile(SessionFile)
	if err != nil {
		return
	}
	var stored map[string]int64
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	now := time.Now()
	sessionMu.Lock()
	defer sessionMu.Unlock()
	for token, expMs := range stored {
		if exp := time.UnixMilli(expMs); exp.After(now) {
			sessions[token] = &session{token: token, expires: exp}
		}
	}
}

// saveSessions writes current sessions to disk. Caller must hold sessionMu.
func saveSessions() {
	if SessionFile == "" {
		return
	}
	stored := make(map[string]int64, len(sessions))
	for token, s := range sessions {
		stored[token] = s.expires.UnixMilli()
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return
	}
	os.WriteFile(SessionFile, data, 0600)
}

// StartSessionCleanup removes expired sessions every hour and saves to disk.
func StartSessionCleanup() {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			sessionMu.Lock()
			for token, s := range sessions {
				if now.After(s.expires) {
					delete(sessions, token)
				}
			}
			saveSessions()
			sessionMu.Unlock()
		}
	}()
}

// HandleLogin authenticates and issues a session cookie.
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	if !appstate.NoAuth && (req.Username != appstate.Username || req.Password != appstate.Password) {
		http.Error(w, `{"error":"Username or password error"}`, http.StatusUnauthorized)
		return
	}

	token, err := generateToken()
	if err != nil {
		http.Error(w, `{"error":"Internal error"}`, http.StatusInternalServerError)
		return
	}

	sessionMu.Lock()
	sessions[token] = &session{token: token, expires: time.Now().Add(sessionTTL)}
	saveSessions()
	sessionMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HandleLogout invalidates the session cookie.
func HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		sessionMu.Lock()
		delete(sessions, cookie.Value)
		saveSessions()
		sessionMu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// AuthMiddleware guards authenticated endpoints. In no-auth mode it passes
// every request through.
func AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if appstate.NoAuth {
			next(w, r)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Error(w, `{"error":"Not logged in"}`, http.StatusUnauthorized)
			return
		}

		sessionMu.RLock()
		sess, ok := sessions[cookie.Value]
		sessionMu.RUnlock()

		if !ok || time.Now().After(sess.expires) {
			if ok {
				sessionMu.Lock()
				delete(sessions, cookie.Value)
				sessionMu.Unlock()
			}
			http.Error(w, `{"error":"Session expired"}`, http.StatusUnauthorized)
			return
		}

		// Extend session on activity.
		sessionMu.Lock()
		sess.expires = time.Now().Add(sessionTTL)
		sessionMu.Unlock()

		next(w, r)
	}
}
