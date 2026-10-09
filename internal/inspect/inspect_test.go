package inspect

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDescribeMasksCredentials(t *testing.T) {
	r := httptest.NewRequest("POST", "http://app.example.com/saml/acs?RelayState=x&token=secret", nil)
	r.Header.Set("Origin", "https://idp.example.org")
	r.Header.Set("Authorization", "Bearer abc")
	r.Header.Set("X-Api-Key", "k")
	r.Header.Set("Cookie", "session=s; lang=it")
	e := Describe(r, net.ParseIP("10.1.2.3"))

	if e.Headers["Origin"] != "https://idp.example.org" {
		t.Errorf("Origin = %q", e.Headers["Origin"])
	}
	if e.Headers["Authorization"] != Masked || e.Headers["X-Api-Key"] != Masked {
		t.Errorf("credentials shown: %v", e.Headers)
	}
	if _, ok := e.Headers["Cookie"]; ok || len(e.Cookies) != 2 || e.Cookies[0] != "lang" {
		t.Errorf("cookies = %v, headers %v", e.Cookies, e.Headers)
	}
	if len(e.Query) != 2 || e.Query[0] != "RelayState" || e.Client != "10.1.2.3" {
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
