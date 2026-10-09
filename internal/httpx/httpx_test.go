package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONResponsesAreNotCached(t *testing.T) {
	tests := []struct {
		name  string
		write func(http.ResponseWriter)
	}{
		{"success", func(w http.ResponseWriter) { WriteJSON(w, http.StatusOK, map[string]bool{"ok": true}) }},
		{"error", func(w http.ResponseWriter) { WriteErr(w, http.StatusBadRequest, "bad") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.write(rec)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}
