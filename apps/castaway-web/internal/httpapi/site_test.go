package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLoginReturnTo(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"/castawordle", "/castawordle"},
		{"/castawordle/123e4567-e89b-12d3-a456-426614174000", "/castawordle/123e4567-e89b-12d3-a456-426614174000"},
		{"/games/button/123e4567-e89b-12d3-a456-426614174000", "/games/button/123e4567-e89b-12d3-a456-426614174000"},
		{"/button", "/button"},
		{"/games/tilt-table", "/games/tilt-table"},
		{"https://evil.example", "/"},
		{"//evil.example", "/"},
		{"/castawordle/../../evil", "/"},
		{"/castawordle/bad-id", "/"},
		{"/castawordle?next=https://evil.example", "/"},
	} {
		if got := loginReturnTo(tc.input); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}

// The service worker must come from the site root (it controls only pages under its own path), and both PWA
// files must load without a session.
func TestPWAFilesArePublic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := New(nil).PublicRouter()
	for path, want := range map[string]string{"/sw.js": "text/javascript", "/manifest.webmanifest": "application/manifest+json"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), want) {
			t.Errorf("%s: status %d, content type %q; want 200 %s", path, recorder.Code, recorder.Header().Get("Content-Type"), want)
		}
	}
}
