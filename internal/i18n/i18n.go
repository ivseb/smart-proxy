// Package i18n translates the pages Smart Proxy shows to the people using the applications (the
// waking page, sign-in pages) into the language of their browser: Italian or English. Texts are
// written in English in the code; the Italian dictionary maps each to its translation.
package i18n

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Lang is a supported language.
type Lang string

const (
	English Lang = "en"
	Italian Lang = "it"
)

// FromRequest picks the language the browser prefers (Accept-Language), English by default.
func FromRequest(r *http.Request) Lang {
	type choice struct {
		lang Lang
		q    float64
	}
	var choices []choice
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		base, _, _ := strings.Cut(strings.ToLower(tag), "-")
		switch Lang(base) {
		case English, Italian:
			if q > 0 {
				choices = append(choices, choice{Lang(base), q})
			}
		}
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].q > choices[j].q })
	if len(choices) > 0 {
		return choices[0].lang
	}
	return English
}

// T translates an English text.
func (l Lang) T(text string) string {
	if l == Italian {
		if s, ok := italian[text]; ok {
			return s
		}
	}
	return text
}

// With translates a text and fills its {name} placeholder.
func (l Lang) With(text, name, value string) string {
	return strings.ReplaceAll(l.T(text), "{"+name+"}", value)
}

// Texts translates several texts at once, e.g. for a page's script.
func (l Lang) Texts(texts ...string) map[string]string {
	m := make(map[string]string, len(texts))
	for _, s := range texts {
		m[s] = l.T(s)
	}
	return m
}

var italian = map[string]string{
	// Waking page
	"Waking up…": "Risveglio in corso…",
	"Waking up":  "Si sta svegliando",
	"This application was asleep to save resources. It's starting now: this page reloads by itself when it's ready.": "Questa applicazione dormiva per risparmiare risorse. Si sta avviando: la pagina si ricarica da sola quando è pronta.",
	"Starting":                   "Avvio",
	"Ready":                      "Pronta",
	"Starting…":                  "In avvio…",
	"Waiting":                    "In attesa",
	"Error":                      "Errore",
	"Checking…":                  "Verifica…",
	"Ready! Opening…":            "Pronta! Apertura…",
	"Connection lost, retrying…": "Connessione persa, nuovo tentativo…",
	"Connecting…":                "Connessione…",

	// Sign-in page of a protected route
	"Sign in":                  "Accedi",
	"to {host}":                "{host}",
	"Name":                     "Nome",
	"Password":                 "Password",
	"Protected by Smart Proxy": "Protetto da Smart Proxy",
	"Nobody can sign in yet: add a user to this route in Smart Proxy's dashboard.": "Nessuno può ancora accedere: aggiungi una persona a questa route dalla dashboard di Smart Proxy.",
	"Sign in from this page.":                                     "Accedi da questa pagina.",
	"Too many attempts. Wait a few minutes and try again.":        "Troppi tentativi. Attendi qualche minuto e riprova.",
	"Wrong name or password.":                                     "Nome o password errati.",
	"Wrong password.":                                             "Password errata.",
	"Sign-in is unavailable: Smart Proxy's Secret can't be read.": "Accesso non disponibile: il Secret di Smart Proxy non è leggibile.",

	// Dashboard sign-in
	"Access token":                   "Token di accesso",
	"Invalid token":                  "Token non valido",
	"Sign-in failed or not allowed.": "Accesso non riuscito o non consentito.",
	"You have been signed out.":      "Sei uscito.",
}
