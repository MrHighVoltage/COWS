package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersRestrictResourcesAndKeepSameHostWebSockets(t *testing.T) {
	for _, test := range []struct {
		name, host, wantConnect string
	}{
		{"domain", "cows.example.test", "'self' ws://cows.example.test wss://cows.example.test"},
		{"development port", "127.0.0.1:8080", "'self' ws://127.0.0.1:8080 wss://127.0.0.1:8080"},
		{"IPv6 self fallback", "[::1]:8080", "'self'"},
		{"injected directive", "cows.test; script-src *", "'self'"},
		{"wildcard", "*.example.test", "'self'"},
		{"userinfo", "attacker@cows.test", "'self'"},
		{"path", "cows.test/path", "'self'"},
		{"newline", "cows.test\r\nX-Injected: true", "'self'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://cows.example.test/", nil)
			r.Host = test.host
			// HTTPS proxies preserve Host; untrusted forwarded values must not
			// add a different origin or determine the allowed schemes.
			r.Header.Set("X-Forwarded-Host", "attacker.test")
			r.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			})).ServeHTTP(w, r)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d", w.Code)
			}
			policy := w.Header().Get("Content-Security-Policy")
			directives := make(map[string]string)
			for _, directive := range strings.Split(policy, "; ") {
				name, values, _ := strings.Cut(directive, " ")
				if _, duplicate := directives[name]; duplicate {
					t.Fatalf("duplicate CSP directive %q", name)
				}
				directives[name] = values
			}
			for name, want := range map[string]string{
				"default-src": "'none'", "base-uri": "'none'", "object-src": "'none'", "frame-ancestors": "'none'",
				"form-action": "'self'", "script-src": "'self' 'unsafe-eval'", "script-src-attr": "'none'",
				"style-src": "'self' 'unsafe-inline'", "img-src": "'self' data:", "font-src": "'self'", "connect-src": test.wantConnect,
			} {
				if directives[name] != want {
					t.Fatalf("%s = %q, want %q", name, directives[name], want)
				}
			}
			if len(directives) != 11 {
				t.Fatalf("unexpected CSP directives: %+v", directives)
			}
		})
	}
}
