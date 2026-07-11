package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cows-project/cows/internal/database"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	server, err := New(db)
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	return server
}

func TestHomeRendersLocalServerDrivenPage(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	testServer(t).Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	for _, expected := range []string{"COWS", "/static/vendor/htmx/htmx.min.js", "/static/vendor/alpine/alpine.min.js", `hx-get="/fragments/health"`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Errorf("page does not contain %q", expected)
		}
	}
}

func TestHealthEndpoint(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	testServer(t).Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if response["status"] != "ok" || response["database"] != "ok" {
		t.Fatalf("unexpected health response: %#v", response)
	}
}

func TestHealthFragmentRendersHTML(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/fragments/health", nil)
	testServer(t).Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `id="health-status"`) {
		t.Fatalf("unexpected fragment response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
