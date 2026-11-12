package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"wxcode/internal/store"
)

func TestAPIOnly(t *testing.T) {
	t.Setenv("API_ONLY", "1")
	t.Setenv("API_CORS_ORIGINS", "https://client.example")
	if err := store.Open(":memory:"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close(); store.InvalidateSettings() })
	h := NewRouter()

	for _, path := range []string{"/admin", "/login", "/api/me", "/api/admin/settings"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, w.Code)
		}
	}
	for _, path := range []string{"/", "/icon.png", "/vendor/bootstrap.min.css", "/layer.js", "/api/health", "/api/docs.md", "/api/openapi.json", "/api/postman.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", path, w.Code)
		}
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(page.Body.String(), `data-api-only="true"`) {
		t.Error("homepage is missing API-only mode")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/encode", strings.NewReader(`{"text":"hello api mode","format":"png"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://client.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("encode: status %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://client.example" {
		t.Errorf("CORS origin = %q", got)
	}
	decode := httptest.NewRecorder()
	dr := httptest.NewRequest(http.MethodPost, "/api/decode", bytes.NewReader(w.Body.Bytes()))
	dr.Header.Set("Content-Type", "image/png")
	h.ServeHTTP(decode, dr)
	var result struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(decode.Body.Bytes(), &result); err != nil || decode.Code != http.StatusOK || result.Payload != "hello api mode" {
		t.Fatalf("decode: status %d, body %s, error %v", decode.Code, decode.Body.String(), err)
	}
	for _, table := range []string{"users", "records"} {
		var count int
		if err := store.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s count = %d, error %v", table, count, err)
		}
	}
}

func TestDefaultModeRoutes(t *testing.T) {
	t.Setenv("API_ONLY", "0")
	t.Setenv("ADMIN_PASSWORD", "test-password")
	if err := store.Open(filepath.Join(t.TempDir(), "wxcode.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close(); store.InvalidateSettings() })
	h := NewRouter()
	for _, path := range []string{"/", "/login", "/api/me", "/api/health"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", path, w.Code)
		}
	}
	var admins int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM users WHERE role = 'admin'").Scan(&admins); err != nil || admins != 1 {
		t.Errorf("admin count = %d, error %v", admins, err)
	}
}
