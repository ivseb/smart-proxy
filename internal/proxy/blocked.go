package proxy

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"smart-proxy/internal/blackout"
	"smart-proxy/internal/i18n"
	"smart-proxy/internal/inspect"
	"smart-proxy/internal/logger"
	"smart-proxy/internal/metrics"
	"smart-proxy/internal/store"
)

// StartPath, under a route's address, starts its application during blocked hours when its
// settings let browsers do so (POST, from the page offering it).
const StartPath = "/__smart_proxy/start"

//go:embed blocked.html
var blockedHTML string

var blockedTmpl = template.Must(template.New("blocked").Parse(blockedHTML))

// awake reports whether the route's application runs or is starting.
func (h *Handler) awake(route store.RouteConfig) bool {
	if h.serving(route) {
		return true
	}
	replicas, _, err := h.k8sClient.GetDeploymentStatus(route.Namespace, route.Deployment)
	return err == nil && replicas > 0
}

// blockedNow reports whether requests must not wake the route's application now: blocked hours,
// and it isn't running or starting (started by hand, say, it serves as usual).
func (h *Handler) blockedNow(route store.RouteConfig) (blackout.Settings, bool) {
	if h.Blackout == nil {
		return blackout.Settings{}, false
	}
	settings := h.Blackout.Get()
	return settings, settings.Blocked(route, time.Now()) && !h.awake(route)
}

// refuseBlocked answers a request that can't wake the route: browsers get the page offering to
// start it when the route's settings say so, everyone else 503.
func (h *Handler) refuseBlocked(w http.ResponseWriter, r *http.Request, rec *recording, route store.RouteConfig, settings blackout.Settings) {
	if p, _ := settings.Policy(route); p.WhenBlocked == store.WhenBlockedAsk && wantsPage(r) {
		rec.set(inspect.OutcomeOffered)
		h.serveBlockedPage(w, r, route, settings)
		return
	}
	rec.set(inspect.OutcomeBlocked)
	h.answerBlocked(w, route)
}

// serveBlockedPage says the application is off outside its hours, with a button to start it.
func (h *Handler) serveBlockedPage(w http.ResponseWriter, r *http.Request, route store.RouteConfig, settings blackout.Settings) {
	metrics.WakeBlocked(route.Namespace, route.ID)
	l := i18n.FromRequest(r)
	now := time.Now()
	st := settings.State(route, now)
	host := requestHost(r)
	lead := l.With("{host} is off outside the hours set for it.", "host", host)
	if st.Change != nil {
		lead += " " + l.With("It can be started as usual again {when}.", "when", whenText(l, *st.Change, now, settings.Location()))
	}
	path := r.URL.Path
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Smart-Proxy", "blocked")
	// 200, as the waking page: corporate proxies and CDNs replace the body of 5xx answers with
	// their own error page, which would hide the button. X-Smart-Proxy still tells it apart.
	w.WriteHeader(http.StatusOK)
	blockedTmpl.Execute(w, struct {
		L                                        i18n.Lang
		Lead, Message, Note, Action, Path, Query string
	}{
		L: l, Lead: lead, Message: st.Policy.Message,
		Note:   l.With("Once started, it stays on while it's used and goes back to sleep after {idle} without traffic.", "idle", idleText(l, route.EffectiveIdleTimeout())),
		Action: strings.TrimRight(path, "/") + StartPath, Path: path, Query: r.URL.RawQuery,
	})
}

// handleStart starts a route's application during its blocked hours, from the page offering it.
// Only a POST from that page (link previews and crawlers only follow links); anyone reaching the
// page may start it, as anyone could request it outside blocked hours.
func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Smart Proxy: use the button on the page", http.StatusMethodNotAllowed)
		return
	}
	host := requestHost(r)
	if !sameSite(r, host) {
		http.Error(w, "Smart Proxy: cross-site request rejected", http.StatusForbidden)
		return
	}
	path := r.PostFormValue("path")
	if path == "" {
		path = strings.TrimSuffix(r.URL.Path, StartPath)
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		path = "/"
	}
	route, found := h.matchRoute(host, cleanPath(path))
	if !found {
		http.NotFound(w, r)
		return
	}
	user := ""
	if route.Protected(cleanPath(path)) {
		id, ok := h.authenticate(r, route)
		if !ok {
			http.Error(w, "Smart Proxy: sign in first", http.StatusUnauthorized)
			return
		}
		user = id.User
	}
	settings, blocked := h.blockedNow(route)
	if p, _ := settings.Policy(route); blocked && p.WhenBlocked == store.WhenBlockedAsk {
		client := h.TrustedProxies.ClientIP(r)
		if h.Inspect != nil && route.Inspecting(time.Now()) {
			rec := h.record(w, r, client, route)
			rec.setUser(user)
			rec.set(inspect.OutcomeStarted)
			w = rec.writer
			defer rec.finish()
		}
		h.store.UpdateActivity(route.ID)
		h.ensureAwake([]store.RouteConfig{route})
		markStarted(w, r, route)
		metrics.StartedWhileBlocked(route.Namespace, route.ID)
		who := client.String()
		if user != "" {
			who = user + " (" + who + ")"
		}
		logger.Printf("Started %s/%s during its blocked hours, from the page on %s%s, by %s", route.Namespace, route.Deployment, host, path, who)
	}
	target := path
	if q := r.PostFormValue("query"); q != "" {
		target += "?" + q
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// startedFor is how long the browser that pressed "Start it now" is let through the blocked hours.
// Right after the start the caches may not show the application starting yet, and the request
// may reach another replica: without this it would get the page again instead of the waking one
// (which also shows a start that failed).
const startedFor = 2 * time.Minute

func startedCookie(routeID string) string {
	h := fnv.New32a()
	h.Write([]byte(routeID))
	return fmt.Sprintf("sp_started_%08x", h.Sum32())
}

// markStarted remembers, in the browser, that it started the route's application.
func markStarted(w http.ResponseWriter, r *http.Request, route store.RouteConfig) {
	cookie := &http.Cookie{Name: startedCookie(route.ID), Value: strconv.FormatInt(time.Now().Unix(), 10),
		Path: cookiePath(route), HttpOnly: true, MaxAge: int(startedFor.Seconds()), SameSite: http.SameSiteLaxMode}
	cookie.Secure = isHTTPS(r)
	http.SetCookie(w, cookie)
}

// justStarted reports whether the request comes from a browser that started the route's
// application from the blocked-hours page moments ago (only while the page offers the start).
func justStarted(r *http.Request, route store.RouteConfig, settings blackout.Settings) bool {
	if p, _ := settings.Policy(route); p.WhenBlocked != store.WhenBlockedAsk {
		return false
	}
	c, err := r.Cookie(startedCookie(route.ID))
	if err != nil {
		return false
	}
	at, err := strconv.ParseInt(c.Value, 10, 64)
	return err == nil && time.Since(time.Unix(at, 0)) < startedFor
}

// sameSite reports whether a browser request comes from a page of the same host (forms posted
// from elsewhere are refused; requests without the headers, e.g. curl, are let through).
func sameSite(r *http.Request, host string) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
		u, err := url.Parse(origin)
		return err == nil && strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), host)
	}
	return true
}

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// whenText renders a coming time for the page: "today at 06:00", "tomorrow at 06:00",
// "Monday at 06:00", "on 12/10 at 06:00".
func whenText(l i18n.Lang, t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.UTC) }
	days := int(day(t).Sub(day(now)).Hours() / 24)
	clock := t.Format("15:04")
	switch {
	case days <= 0:
		return l.With("today at {time}", "time", clock)
	case days == 1:
		return l.With("tomorrow at {time}", "time", clock)
	case days < 7:
		return strings.NewReplacer("{day}", l.T(weekdayNames[t.Weekday()]), "{time}", clock).Replace(l.T("{day} at {time}"))
	}
	return strings.NewReplacer("{date}", fmt.Sprintf("%d/%d", t.Day(), int(t.Month())), "{time}", clock).Replace(l.T("on {date} at {time}"))
}

// idleText renders an idle timeout: "30 minutes", "1 hour", "1 h 30 min".
func idleText(l i18n.Lang, d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m%60 != 0 && m > 60:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	case m == 60:
		return l.T("1 hour")
	case m%60 == 0:
		return l.With("{n} hours", "n", fmt.Sprint(m/60))
	case m == 1:
		return l.T("1 minute")
	}
	return l.With("{n} minutes", "n", fmt.Sprint(m))
}
