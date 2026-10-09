package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "smart_proxy_session"

// signer produces and verifies tamper-proof cookie values: base64(payload) + "." + base64(HMAC).
type signer struct {
	key []byte
}

func (s signer) sign(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + base64.RawURLEncoding.EncodeToString(s.mac(enc)), nil
}

func (s signer) verify(value string, v any) error {
	enc, sig, ok := strings.Cut(value, ".")
	if !ok {
		return errors.New("malformed cookie")
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(enc)) {
		return errors.New("invalid cookie signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

func (s signer) mac(data string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

type session struct {
	User    string `json:"u"`
	Expires int64  `json:"exp"`
}

func (a *Auth) setSession(w http.ResponseWriter, r *http.Request, user string) error {
	exp := time.Now().Add(a.cfg.SessionTTL)
	value, err := a.signer.sign(session{User: user, Expires: exp.Unix()})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (a *Auth) sessionUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	var s session
	if err := a.signer.verify(c.Value, &s); err != nil || time.Now().Unix() > s.Expires || s.User == "" {
		return "", false
	}
	return s.User, true
}

func clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// isHTTPS reports whether the browser reached us over TLS, directly or through a TLS-terminating proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
