// Package metrics exposes Prometheus metrics about proxied traffic, wake-ups and sleeping
// deployments.
package metrics

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "smart_proxy"

var (
	requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "requests_total",
		Help:      "Requests proxied to applications, by route.",
	}, []string{"namespace", "route"})

	wakeups = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "wakeups_total",
		Help:      "Deployments scaled up from zero, by trigger (request or manual).",
	}, []string{"namespace", "deployment", "trigger"})

	wakeDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "wake_duration_seconds",
		Help:      "Cold start: time from scaling a deployment up until it has a ready replica.",
		Buckets:   []float64{1, 2, 5, 10, 20, 30, 60, 120, 300},
	}, []string{"namespace", "deployment"})

	sleeps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "sleeps_total",
		Help:      "Deployments scaled to zero, by reason (idle or manual).",
	}, []string{"namespace", "deployment", "reason"})

	leader = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "leader",
		Help:      "1 if this replica is the leader (sleeps deployments, heals patches).",
	})

	registry = prometheus.NewRegistry()

	wakeMu      sync.Mutex
	wakeStarted = map[string]time.Time{}
)

func init() {
	registry.MustRegister(requests, wakeups, wakeDuration, sleeps, leader,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// Handler serves the metrics in the Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// Request counts a proxied request.
func Request(namespace, routeID string) {
	requests.WithLabelValues(namespace, routeID).Inc()
}

// WakeStarted records that a deployment was scaled up from zero.
func WakeStarted(namespace, deployment, trigger string) {
	wakeups.WithLabelValues(namespace, deployment, trigger).Inc()
	wakeMu.Lock()
	defer wakeMu.Unlock()
	key := namespace + "/" + deployment
	if _, ok := wakeStarted[key]; !ok {
		wakeStarted[key] = time.Now()
	}
}

// Ready records that a deployment serves again, completing its cold start if one was pending.
func Ready(namespace, deployment string) {
	wakeMu.Lock()
	key := namespace + "/" + deployment
	started, ok := wakeStarted[key]
	delete(wakeStarted, key)
	wakeMu.Unlock()
	if ok {
		wakeDuration.WithLabelValues(namespace, deployment).Observe(time.Since(started).Seconds())
	}
}

// Slept records that a deployment was scaled to zero.
func Slept(namespace, deployment, reason string) {
	sleeps.WithLabelValues(namespace, deployment, reason).Inc()
}

// SetLeader records whether this replica leads.
func SetLeader(isLeader bool) {
	if isLeader {
		leader.Set(1)
	} else {
		leader.Set(0)
	}
}

// SleepingSource reports, per namespace, how many deployments are asleep and how many replicas
// they would run (what Smart Proxy is saving).
type SleepingSource func() (deployments map[string]int, replicas map[string]int)

// RegisterSleeping exposes the sleeping deployments and replicas, computed at scrape time.
// "Replica-hours saved" is then sum_over_time(smart_proxy_sleeping_replicas[1d]) / 60 (with a
// 1m scrape interval), or similar.
func RegisterSleeping(source SleepingSource) {
	registry.MustRegister(&sleepingCollector{source: source})
}

type sleepingCollector struct {
	source SleepingSource
}

var (
	sleepingDeploymentsDesc = prometheus.NewDesc(namespace+"_sleeping_deployments",
		"Deployments currently put to sleep by Smart Proxy.", []string{"namespace"}, nil)
	sleepingReplicasDesc = prometheus.NewDesc(namespace+"_sleeping_replicas",
		"Replicas the sleeping deployments would otherwise run.", []string{"namespace"}, nil)
)

func (c *sleepingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- sleepingDeploymentsDesc
	ch <- sleepingReplicasDesc
}

func (c *sleepingCollector) Collect(ch chan<- prometheus.Metric) {
	deployments, replicas := c.source()
	for ns, n := range deployments {
		ch <- prometheus.MustNewConstMetric(sleepingDeploymentsDesc, prometheus.GaugeValue, float64(n), ns)
	}
	for ns, n := range replicas {
		ch <- prometheus.MustNewConstMetric(sleepingReplicasDesc, prometheus.GaugeValue, float64(n), ns)
	}
}

// SleepingFromAnnotations is a helper turning "replicas-before-sleep" annotation values into counts.
func SleepingFromAnnotations(namespaces []string, recorded []string) (map[string]int, map[string]int) {
	deployments, replicas := map[string]int{}, map[string]int{}
	for i, ns := range namespaces {
		deployments[ns]++
		n, err := strconv.Atoi(recorded[i])
		if err != nil || n < 1 {
			n = 1
		}
		replicas[ns] += n
	}
	return deployments, replicas
}
