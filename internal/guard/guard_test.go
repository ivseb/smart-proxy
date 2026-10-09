package guard

import (
	"strings"
	"testing"

	"smart-proxy/internal/store"
)

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") || strings.Contains(hash, "correct horse") {
		t.Fatalf("hash = %q", hash)
	}
	if !CheckPassword(hash, "correct horse") || CheckPassword(hash, "correct horse ") || CheckPassword("garbage", "x") {
		t.Fatal("CheckPassword")
	}
	other, _ := HashPassword("correct horse")
	if other == hash {
		t.Fatal("no salt")
	}
}

func TestTokens(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || !strings.HasPrefix(a, "spt_") || len(a) < 30 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if HashToken(a) == HashToken(b) || HashToken(a) != HashToken(a) {
		t.Fatal("HashToken")
	}
}

func TestSafeNext(t *testing.T) {
	for next, want := range map[string]string{"/orders?x=1": "/orders?x=1", "//evil.com": "/", "https://evil.com": "/", "/\\evil.com": "/", "": "/"} {
		if got := safeNext(next, routeAt("/")); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", next, got, want)
		}
	}
}

func routeAt(path string) store.RouteConfig { return store.RouteConfig{ID: "r", Path: path} }
