package i18n

import (
	"net/http/httptest"
	"testing"
)

func TestFromRequest(t *testing.T) {
	for header, want := range map[string]Lang{
		"":                                    English,
		"it-IT,it;q=0.9,en-US;q=0.8,en;q=0.7": Italian,
		"en-US,en;q=0.9,it;q=0.8":             English,
		"de-DE,de;q=0.9,it;q=0.5":             Italian,
		"fr":                                  English,
		"en;q=0.3, IT;q=0.6":                  Italian,
		"it;q=0, en":                          English,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", header)
		if got := FromRequest(r); got != want {
			t.Errorf("%q: %s, want %s", header, got, want)
		}
	}
}

func TestT(t *testing.T) {
	if Italian.T("Sign in") != "Accedi" || English.T("Sign in") != "Sign in" || Italian.T("not translated") != "not translated" {
		t.Fatal("translation")
	}
}
