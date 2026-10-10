package inspect

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDescribeMasksCredentials(t *testing.T) {
	r := httptest.NewRequest("POST", "http://app.example.com/checkout/return?order=x&token=secret", nil)
	r.Header.Set("Origin", "https://partner.example.org")
	r.Header.Set("Authorization", "Bearer abc")
	r.Header.Set("X-Api-Key", "k")
	r.Header.Set("Cookie", "session=s; lang=it")
	e := Describe(r, net.ParseIP("10.1.2.3"))

	if e.Headers["Origin"] != "https://partner.example.org" {
		t.Errorf("Origin = %q", e.Headers["Origin"])
	}
	if e.Headers["Authorization"] != Masked || e.Headers["X-Api-Key"] != Masked {
		t.Errorf("credentials shown: %v", e.Headers)
	}
	if _, ok := e.Headers["Cookie"]; ok || len(e.Cookies) != 2 || e.Cookies[0] != "lang" {
		t.Errorf("cookies = %v, headers %v", e.Cookies, e.Headers)
	}
	if len(e.Query) != 2 || e.Query[0] != "order" || e.Client != "10.1.2.3" {
		t.Errorf("query = %v, client %q", e.Query, e.Client)
	}
}

func TestRecorderKeepsTheLatestAndMerges(t *testing.T) {
	a := &Recorder{Replica: "a"}
	t0 := time.Now()
	for i := 0; i < perRoute+10; i++ {
		a.Add("r", Entry{At: t0.Add(time.Duration(i) * time.Millisecond), Path: "/a"})
	}
	if got := a.Since("r", time.Time{}); len(got) != perRoute || got[0].Replica != "a" {
		t.Fatalf("kept %d", len(got))
	}
	b := []Entry{{At: t0.Add(-time.Second), Path: "/b"}, {At: t0.Add(time.Hour), Path: "/b"}}
	merged := Merge(5, a.Since("r", time.Time{}), b)
	if len(merged) != 5 || merged[4].Path != "/b" {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestPeerHandlerNeedsTheToken(t *testing.T) {
	rec := &Recorder{Replica: "a"}
	rec.Add("r", Entry{At: time.Now()})
	h := rec.PeerHandler(func() string { return "s3cret" })
	for token, want := range map[string]int{"": 401, "wrong": 401, "s3cret": 200} {
		r := httptest.NewRequest("GET", PeerPath+"?id=r", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("token %q: %d", token, w.Code)
		}
	}
	// No token configured: nobody gets in.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", PeerPath+"?id=r", nil)
	r.Header.Set("Authorization", "Bearer ")
	rec.PeerHandler(func() string { return "" }).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty token accepted")
	}
}

func TestMoreCredentialHeadersAreMasked(t *testing.T) {
	r := httptest.NewRequest("GET", "http://x/", nil)
	for _, h := range []string{"Authentication", "X-Auth-Key", "X-Access-Key", "Ocp-Apim-Subscription-Key", "Cf-Access-Jwt-Assertion",
		"X-Amzn-Oidc-Data", "X-Ms-Client-Principal", "X-Forwarded-Client-Cert", "X-Csrf-Token"} {
		r.Header.Set(h, "secret-value")
	}
	r.Header.Set("Origin", "https://partner.example")
	e := Describe(r, nil)
	for name, value := range e.Headers {
		if value == "secret-value" {
			t.Errorf("%s recorded in clear", name)
		}
	}
	if e.Headers["Origin"] != "https://partner.example" {
		t.Error("Origin masked")
	}
}

func TestRecordedEntriesAreBounded(t *testing.T) {
	r := httptest.NewRequest("GET", "http://x/"+strings.Repeat("a", 5000), nil)
	for i := 0; i < 500; i++ {
		r.Header.Set(fmt.Sprintf("X-H%d", i), strings.Repeat("v", 1000))
	}
	e := Describe(r, nil)
	if len(e.Path) > 510 || len(e.Headers) > maxNames {
		t.Fatalf("path %d chars, %d headers", len(e.Path), len(e.Headers))
	}
}

func TestStatusIgnoresEarlyHints(t *testing.T) {
	w := &StatusWriter{ResponseWriter: httptest.NewRecorder()}
	w.WriteHeader(http.StatusEarlyHints)
	w.WriteHeader(http.StatusInternalServerError)
	if w.Status != 500 {
		t.Fatalf("status = %d", w.Status)
	}
}
