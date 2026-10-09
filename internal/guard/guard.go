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
	"net"
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

// CheckPassword compares a password with a stored hash.
func CheckPassword(hash, password string) bool {
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
func (g *Guard) Authenticate(r *http.Request, route store.RouteConfig) (Identity, bool) {
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
	if user, password, ok := r.BasicAuth(); ok {
		if g.checkUser(route.ID, creds, user, password) {
			return Identity{User: user, header: "Authorization"}, true
		}
	}
	if c, err := r.Cookie(CookieName(route.ID)); err == nil {
		var s session
		if g.verify(c.Value, &s) == nil && s.Route == route.ID && time.Now().Unix() < s.Expires {
			if _, exists := creds.Users[s.User]; exists { // Removed users are logged out
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
func Strip(r *http.Request, route store.RouteConfig, id Identity) {
	if id.header != "" {
		r.Header.Del(id.header)
	}
	name := CookieName(route.ID)
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
	r.Header.Set(UserHeader, id.User)
}

type session struct {
	Route   string `json:"r"`
	User    string `json:"u"`
	Expires int64  `json:"exp"`
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

// tooManyAttempts limits failed logins per client: 10 in 5 minutes.
func (g *Guard) tooManyAttempts(client string, failed bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.attempts == nil || len(g.attempts) > 10000 {
		g.attempts = map[string][]time.Time{}
	}
	now := time.Now()
	recent := g.attempts[client][:0]
	for _, t := range g.attempts[client] {
		if now.Sub(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	if failed {
		recent = append(recent, now)
	}
	g.attempts[client] = recent
	return len(recent) >= 10
}

func clientKey(r *http.Request, client net.IP) string {
	if client != nil {
		return client.String()
	}
	return r.RemoteAddr
}
