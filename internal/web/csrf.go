package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) csrfSignature(r *http.Request, nonce string) string {
	mac := hmac.New(sha256.New, s.csrfKey[:])
	if session, err := r.Cookie(sessionCookieName); err == nil {
		mac.Write([]byte(session.Value))
	}
	mac.Write([]byte{0})
	mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) signedCSRF(r *http.Request, token string) bool {
	nonce, signature, ok := strings.Cut(token, ".")
	if !ok || len(nonce) != 43 || len(signature) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(decoded) != 32 {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(s.csrfSignature(r, nonce)))
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && s.signedCSRF(r, cookie.Value) {
		return cookie.Value
	}
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ""
	}
	nonce := base64.RawURLEncoding.EncodeToString(bytes[:])
	token := nonce + "." + s.csrfSignature(r, nonce)
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", MaxAge: int(s.options.SessionLifetime.Seconds()), HttpOnly: true, Secure: s.options.CookieSecure, SameSite: http.SameSiteLaxMode})
	return token
}

func (s *Server) sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site == "same-site" {
		// Same-site sibling hosts are not trusted to authorize COWS mutations.
		if r.Header.Get("Origin") == "" {
			return false
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil || s.options.CookieSecure {
			scheme = "https"
		}
		return err == nil && parsed.Scheme == scheme && strings.EqualFold(parsed.Host, r.Host) && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
	}
	return true
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || !s.sameOrigin(r) || !s.signedCSRF(r, cookie.Value) {
		return false
	}
	provided := r.FormValue("csrf_token")
	return provided != "" && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(provided)) == 1
}
