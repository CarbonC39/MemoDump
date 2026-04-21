package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"
)

type session struct {
	token   string
	expires time.Time
}

var (
	sessions    = make(map[string]*session)
	sessionMu   sync.RWMutex
	sessionTTL  = 30 * 24 * time.Hour // 30 days
	sessionFile string
)

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// loadSessions reads persisted sessions from disk, discarding already-expired ones.
func loadSessions() {
	if sessionFile == "" {
		return
	}
	data, err := os.ReadFile(sessionFile)
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
	if sessionFile == "" {
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
	os.WriteFile(sessionFile, data, 0600)
}

// startSessionCleanup removes expired sessions every hour and saves to disk.
func startSessionCleanup() {
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	if req.Username != username || req.Password != password {
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

func handleLogout(w http.ResponseWriter, r *http.Request) {
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

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		// Extend session on activity
		sessionMu.Lock()
		sess.expires = time.Now().Add(sessionTTL)
		sessionMu.Unlock()

		next(w, r)
	}
}
