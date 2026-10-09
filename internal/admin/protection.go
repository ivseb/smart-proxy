package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"smart-proxy/internal/guard"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/vault"
)

// Credential is a user or access token of a protected route, as listed (never its secret).
type Credential struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Hint    string    `json:"hint,omitempty"`
}

func (s *Server) vault() *vault.Vault {
	if s.Vault == nil {
		return nil
	}
	return s.Vault()
}

// credentials lists a route's users and tokens.
func (s *Server) credentials(routeID string) (users, tokens []Credential) {
	creds := s.vault().Credentials(routeID)
	list := func(m map[string]vault.Secret) []Credential {
		out := []Credential{}
		for name, secret := range m {
			out = append(out, Credential{Name: name, Created: secret.Created, Hint: secret.Hint})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	return list(creds.Users), list(creds.Tokens)
}

func validName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 64 && !strings.ContainsAny(name, ":\n\r\t")
}

// handleProtectionUsers adds or replaces (POST {id, name, password}) or removes (DELETE ?id&name)
// a user who may sign in to a route.
func (s *Server) handleProtectionUsers(w http.ResponseWriter, r *http.Request) {
	v := s.vault()
	switch r.Method {
	case http.MethodPost:
		var req struct{ ID, Name, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if _, ok := s.store.GetRoute(req.ID); !ok {
			http.Error(w, "Route not found", http.StatusNotFound)
			return
		}
		if !validName(req.Name) {
			http.Error(w, "A name of up to 64 characters, without colons", http.StatusBadRequest)
			return
		}
		if utf8.RuneCountInString(req.Password) < 8 {
			http.Error(w, "The password needs at least 8 characters", http.StatusBadRequest)
			return
		}
		hash, err := guard.HashPassword(req.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		err = v.UpdateCredentials(r.Context(), req.ID, func(c *vault.Credentials) {
			if c.Users == nil {
				c.Users = map[string]vault.Secret{}
			}
			c.Users[req.Name] = vault.Secret{Hash: hash, Created: time.Now()}
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		logger.Printf("Route %s: user %q can sign in", req.ID, req.Name)
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		id, name := r.URL.Query().Get("id"), r.URL.Query().Get("name")
		if err := v.UpdateCredentials(r.Context(), id, func(c *vault.Credentials) { delete(c.Users, name) }); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		logger.Printf("Route %s: user %q removed (signed out)", id, name)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleProtectionTokens creates (POST {id, name}, answering the token once) or revokes
// (DELETE ?id&name) an access token of a route.
func (s *Server) handleProtectionTokens(w http.ResponseWriter, r *http.Request) {
	v := s.vault()
	switch r.Method {
	case http.MethodPost:
		var req struct{ ID, Name string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if _, ok := s.store.GetRoute(req.ID); !ok {
			http.Error(w, "Route not found", http.StatusNotFound)
			return
		}
		if !validName(req.Name) {
			http.Error(w, "A name of up to 64 characters, without colons", http.StatusBadRequest)
			return
		}
		token := guard.NewToken()
		err := v.UpdateCredentials(r.Context(), req.ID, func(c *vault.Credentials) {
			if c.Tokens == nil {
				c.Tokens = map[string]vault.Secret{}
			}
			c.Tokens[req.Name] = vault.Secret{Hash: guard.HashToken(token), Created: time.Now(), Hint: token[len(token)-4:]}
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		logger.Printf("Route %s: access token %q created", req.ID, req.Name)
		writeJSON(w, map[string]string{"token": token})

	case http.MethodDelete:
		id, name := r.URL.Query().Get("id"), r.URL.Query().Get("name")
		if err := v.UpdateCredentials(r.Context(), id, func(c *vault.Credentials) { delete(c.Tokens, name) }); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		logger.Printf("Route %s: access token %q revoked", id, name)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
