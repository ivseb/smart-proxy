// Package guard protects routes: browsers log in on a page served by Smart Proxy on the
// application's own host, scripts send an access token. Passwords and tokens are kept hashed
// in Smart Proxy's Secret (package vault); a login is a signed cookie scoped to the route.
package guard

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"smart-proxy/internal/store"
	"smart-proxy/internal/vault"
)

// Paths served by Smart Proxy under a protected route's path.
const (
	LoginPath  = "/__smart_proxy/login"
	LogoutPath = "/__smart_proxy/logout"
)

// UserHeader tells the application who the request comes from: a user name, or
// "token:<name>" for an access token. Clients can't set it: it is always replaced.
const UserHeader = "X-Smart-Proxy-User"

// TokenHeader is an alternative to "Authorization: Bearer", for applications using that header
// themselves.
const TokenHeader = "X-Api-Key"

const iterations = 120_000 // PBKDF2-SHA256: slow enough to guess, fast enough to log in

// HashPassword hashes a password for storage.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// hashing bounds the password checks running at once: each takes tens of milliseconds of CPU,
// and the proxy must keep serving every application meanwhile.
var hashing = make(chan struct{}, 4)

// dummyHash is checked for unknown users, so they take as long as known ones.
var dummyHash, _ = HashPassword("smart-proxy: no such user")

// CheckPassword compares a password with a stored hash.
func CheckPassword(hash, password string) bool {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || err1 != nil || err2 != nil || iter < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns a fresh access token.
func NewToken() string { return "spt_" + vault.Random(24) }

// HashToken hashes an access token (random, so a plain hash is enough).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Guard checks credentials against the routes' secrets.
type Guard struct {
	Vault *vault.Vault

	mu       sync.Mutex
	verified map[string]time.Time   // Recently checked Basic credentials (hashing is slow) -> until
	attempts map[string][]time.Time // Client -> recent failed logins
}

// Identity is who a request comes from, and which of its parts carried the credential (to be
// removed before the request reaches the application).
type Identity struct {
	User   string
	header string // "Authorization", TokenHeader or "" for the session cookie
}

// CookieName is the login cookie of a route.
func CookieName(routeID string) string {
	h := fnv.New32a()
	h.Write([]byte(routeID))
	return fmt.Sprintf("sp_auth_%08x", h.Sum32())
}

// Authenticate finds valid credentials in a request: an access token, a user's Basic
// credentials, or a login cookie.
func (g *Guard) Authenticate(r *http.Request, route store.RouteConfig, client string) (Identity, bool) {
	creds := g.Vault.Credentials(route.ID)
	if token := bearer(r); token != "" {
		if name, ok := matchToken(creds, token); ok {
			return Identity{User: "token:" + name, header: "Authorization"}, true
		}
	}
	if token := r.Header.Get(TokenHeader); token != "" {
		if name, ok := matchToken(creds, token); ok {
			return Identity{User: "token:" + name, header: TokenHeader}, true
		}
	}
	if user, password, ok := r.BasicAuth(); ok && !g.limited(route.ID, client, user) {
		if g.checkUser(route.ID, creds, user, password) {
			return Identity{User: user, header: "Authorization"}, true
		}
		g.failed(route.ID, client, user)
	}
	if c, err := r.Cookie(CookieName(route.ID)); err == nil {
		var s session
		if g.verify(c.Value, &s) == nil && s.Route == route.ID && time.Now().Unix() < s.Expires {
			// Removed users, and sessions opened with a password changed since, are signed out.
			if secret, exists := creds.Users[s.User]; exists && s.Secret == fingerprint(secret.Hash) {
				return Identity{User: s.User}, true
			}
		}
	}
	return Identity{}, false
}

func bearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

func matchToken(creds vault.Credentials, token string) (string, bool) {
	hash := HashToken(token)
	for name, t := range creds.Tokens {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(t.Hash)) == 1 {
			return name, true
		}
	}
	return "", false
}

// checkUser verifies a user's password, remembering successes for a few minutes: scripts using
// Basic credentials send them with every request.
func (g *Guard) checkUser(routeID string, creds vault.Credentials, user, password string) bool {
	secret, ok := creds.Users[user]
	if !ok {
		CheckPassword(dummyHash, password) // Same time as for a real user: names can't be probed
		return false
	}
	sum := sha256.Sum256([]byte(routeID + "\x00" + user + "\x00" + password + "\x00" + secret.Hash))
	key := hex.EncodeToString(sum[:])
	g.mu.Lock()
	until, cached := g.verified[key]
	g.mu.Unlock()
	if cached && time.Now().Before(until) {
		return true
	}
	if !CheckPassword(secret.Hash, password) {
		return false
	}
	g.mu.Lock()
	if g.verified == nil || len(g.verified) > 10000 {
		g.verified = map[string]time.Time{}
	}
	g.verified[key] = time.Now().Add(5 * time.Minute)
	g.mu.Unlock()
	return true
}

// Strip removes the credentials from a request before it reaches the application, and tells
// it who the request comes from.
func Strip(r *http.Request, id Identity) {
	if id.header != "" {
		r.Header.Del(id.header)
	}
	Scrub(r)
	r.Header.Set(UserHeader, id.User)
}

// Scrub removes what only Smart Proxy may say or see, from every request: the user header in
// any spelling ("X_Smart_Proxy_User" reaches CGI-style servers as the same variable), a
// Connection header asking to drop it, and Smart Proxy's login cookies (another application on
// the host must not receive them).
func Scrub(r *http.Request) {
	for name := range r.Header {
		if strings.EqualFold(strings.ReplaceAll(name, "_", "-"), UserHeader) {
			delete(r.Header, name)
		}
	}
	if values := r.Header.Values("Connection"); len(values) > 0 {
		var keep []string
		for _, v := range values {
			for _, token := range strings.Split(v, ",") {
				if t := strings.TrimSpace(token); t != "" && !strings.EqualFold(strings.ReplaceAll(t, "_", "-"), UserHeader) {
					keep = append(keep, t)
				}
			}
		}
		r.Header.Del("Connection")
		if len(keep) > 0 {
			r.Header.Set("Connection", strings.Join(keep, ", "))
		}
	}
	if raw := r.Header.Values("Cookie"); len(raw) > 0 {
		var keep []string
		for _, line := range raw {
			for _, part := range strings.Split(line, ";") {
				name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
				if part = strings.TrimSpace(part); part != "" && !strings.HasPrefix(name, "sp_auth_") {
					keep = append(keep, part) // As sent: values are not re-encoded
				}
			}
		}
		r.Header.Del("Cookie")
		if len(keep) > 0 {
			r.Header.Set("Cookie", strings.Join(keep, "; "))
		}
	}
}

type session struct {
	Route   string `json:"r"`
	User    string `json:"u"`
	Expires int64  `json:"exp"`
	Secret  string `json:"s"` // Fingerprint of the password it was opened with: changing it signs out
}

// fingerprint identifies a stored password hash without revealing it.
func fingerprint(hash string) string {
	sum := sha256.Sum256([]byte("session\x00" + hash))
	return hex.EncodeToString(sum[:8])
}

func (g *Guard) sign(v any) (string, error) {
	key := g.Vault.LoginKey()
	if len(key) == 0 {
		return "", errors.New("no signing key")
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac(key, enc)), nil
}

func (g *Guard) verify(value string, v any) error {
	key := g.Vault.LoginKey()
	enc, sig, ok := strings.Cut(value, ".")
	if !ok || len(key) == 0 {
		return errors.New("malformed")
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(key, enc)) {
		return errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// Failed sign-ins are limited per client (10 in 5 minutes) and per route and user (30 in 5
// minutes: many clients guessing one shared password).
const (
	failWindow    = 5 * time.Minute
	maxPerClient  = 10
	maxPerAccount = 30
)

func (g *Guard) recent(key string, add bool) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.attempts == nil {
		g.attempts = map[string][]time.Time{}
	}
	if len(g.attempts) > 10000 { // Forget stale entries only: wiping all would reset everyone's count
		for k, times := range g.attempts {
			if len(times) == 0 || now.Sub(times[len(times)-1]) > failWindow {
				delete(g.attempts, k)
			}
		}
	}
	list := g.attempts[key][:0]
	for _, t := range g.attempts[key] {
		if now.Sub(t) < failWindow {
			list = append(list, t)
		}
	}
	if add {
		list = append(list, now)
	}
	if len(list) == 0 {
		delete(g.attempts, key)
	} else {
		g.attempts[key] = list
	}
	return len(list)
}

// limited reports whether sign-ins are refused for now, for this client or this account.
func (g *Guard) limited(routeID, client, user string) bool {
	return g.recent("client "+routeID+" "+client, false) >= maxPerClient ||
		g.recent("account "+routeID+" "+user, false) >= maxPerAccount
}

func (g *Guard) failed(routeID, client, user string) {
	g.recent("client "+routeID+" "+client, true)
	g.recent("account "+routeID+" "+user, true)
}
