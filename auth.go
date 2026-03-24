package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type session struct {
	token   string
	expires time.Time
}

var (
	sessions   = make(map[string]*session)
	sessionMu  sync.RWMutex
	sessionTTL = 24 * time.Hour
)

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
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

	token := generateToken()
	sessionMu.Lock()
	sessions[token] = &session{token: token, expires: time.Now().Add(sessionTTL)}
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

		// Extend session
		sessionMu.Lock()
		sess.expires = time.Now().Add(sessionTTL)
		sessionMu.Unlock()

		next(w, r)
	}
}
