package traffic

import (
	"sort"
	"time"
)

// RuleHit counts the requests one ignore rule matched on a route, by hour, over the last day:
// shown next to each rule so unused or too broad rules stand out.
type RuleHit struct {
	Reason string          `json:"reason"` // ReasonPath, ReasonUserAgent…
	Rule   string          `json:"rule"`   // The pattern that matched, e.g. "/health*"
	Hours  map[int64]int64 `json:"hours"`  // Unix hour -> requests
	Last   time.Time       `json:"last"`
}

const maxRulesPerRoute = 64

func (h RuleHit) key() string { return h.Reason + " " + h.Rule }

// Total is the number of requests matched over the last 24 hours.
func (h RuleHit) Total(now time.Time) int64 {
	var n int64
	from := now.Add(-forgetAfter).Unix() / 3600
	for hour, count := range h.Hours {
		if hour > from {
			n += count
		}
	}
	return n
}

// RecordRule notes a request ignored by a rule of a route.
func (r *Recorder) RecordRule(routeID, reason, rule string) {
	if reason == "" {
		return
	}
	now := time.Now()
	hour := now.Unix() / 3600
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rules == nil {
		r.rules = map[string]map[string]*RuleHit{}
	}
	hits := r.rules[routeID]
	if hits == nil {
		hits = map[string]*RuleHit{}
		r.rules[routeID] = hits
	}
	h := RuleHit{Reason: reason, Rule: truncate(rule, maxPathLength)}
	entry, ok := hits[h.key()]
	if !ok {
		if len(hits) >= maxRulesPerRoute {
			return // Bounded: rules are a short list, a flood of distinct ones is not worth keeping
		}
		h.Hours = map[int64]int64{}
		entry = &h
		hits[h.key()] = entry
	}
	entry.Hours[hour]++
	entry.Last = now
	for old := range entry.Hours {
		if old <= hour-24 {
			delete(entry.Hours, old)
		}
	}
}

// RuleSnapshot returns every route's rule hits of the last day.
func (r *Recorder) RuleSnapshot() map[string][]RuleHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make(map[string][]RuleHit, len(r.rules))
	for id, hits := range r.rules {
		for k, h := range hits {
			if now.Sub(h.Last) > forgetAfter {
				delete(hits, k)
				continue
			}
			c := *h
			c.Hours = make(map[int64]int64, len(h.Hours))
			for hour, n := range h.Hours {
				c.Hours[hour] = n
			}
			out[id] = append(out[id], c)
		}
	}
	return out
}

// MergeRuleHits combines the rule hits seen by several replicas, most matched first.
func MergeRuleHits(lists ...[]RuleHit) []RuleHit {
	merged := map[string]*RuleHit{}
	for _, list := range lists {
		for _, h := range list {
			m, ok := merged[h.key()]
			if !ok {
				c := h
				c.Hours = map[int64]int64{}
				merged[h.key()] = &c
				m = &c
			} else if h.Last.After(m.Last) {
				m.Last = h.Last
			}
			for hour, n := range h.Hours {
				m.Hours[hour] += n
			}
		}
	}
	now := time.Now()
	out := make([]RuleHit, 0, len(merged))
	for _, h := range merged {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Total(now), out[j].Total(now); a != b {
			return a > b
		}
		return out[i].key() < out[j].key()
	})
	return out
}
