package uptime

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"smart-proxy/internal/logger"
)

const dataKey = "uptime.json.gz"

// Store keeps the history in a ConfigMap of Smart Proxy's namespace.
type Store struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string
	Labels    map[string]string
	Owner     *metav1.OwnerReference // Deleted with it (Smart Proxy's Deployment)
}

// Load reads the history (empty when there is none yet).
func (s *Store) Load(ctx context.Context) ([]Series, error) {
	var series []Series
	return series, s.load(ctx, s.Name, &series)
}

// LoadYear reads the year's history, kept in a ConfigMap of its own (<Name>-year).
func (s *Store) LoadYear(ctx context.Context) ([]YearSeries, error) {
	var year []YearSeries
	return year, s.load(ctx, s.Name+"-year", &year)
}

// Save writes the history.
func (s *Store) Save(ctx context.Context, series []Series) error { return s.save(ctx, s.Name, series) }

// SaveYear writes the year's history.
func (s *Store) SaveYear(ctx context.Context, year []YearSeries) error {
	return s.save(ctx, s.Name+"-year", year)
}

func (s *Store) load(ctx context.Context, name string, v any) error {
	cm, err := s.Client.CoreV1().ConfigMaps(s.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	data := cm.BinaryData[dataKey]
	if len(data) == 0 {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *Store) save(ctx context.Context, name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	cms := s.Client.CoreV1().ConfigMaps(s.Namespace)
	cm, err := cms.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: s.Labels},
			BinaryData: map[string][]byte{dataKey: buf.Bytes()}}
		if s.Owner != nil {
			cm.OwnerReferences = []metav1.OwnerReference{*s.Owner}
		}
		_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if cm.BinaryData == nil {
		cm.BinaryData = map[string][]byte{}
	}
	cm.BinaryData[dataKey] = buf.Bytes()
	_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

// Keeper runs the recording: the leader samples every minute and saves every few minutes; the
// other replicas reload what it saved, to answer the dashboard.
type Keeper struct {
	Recorder *Recorder
	Store    *Store
	// Sample returns the managed workloads and their replicas (the leader only calls it).
	Sample func() []State
	// Leading reports whether this replica is the leader.
	Leading func() bool
}

// Run records until ctx ends.
func (k *Keeper) Run(ctx context.Context) {
	if err := k.load(ctx); err != nil {
		logger.Printf("Warning: loading the uptime history: %v", err)
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	wasLeading := false
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			if wasLeading && k.Recorder.TakeDirty() { // Shutting down: keep the latest minutes
				saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				k.save(saveCtx)
			}
			return
		case <-tick.C:
		}
		leading := k.Leading()
		if leading != wasLeading || !leading {
			// The other replicas follow what the leader saves; a new leader continues from it.
			k.load(ctx)
		}
		wasLeading = leading
		if !leading {
			continue
		}
		k.Recorder.Sample(time.Now(), k.Sample())
		if n%5 == 0 && k.Recorder.TakeDirty() {
			if err := k.save(ctx); err != nil {
				logger.Printf("Warning: saving the uptime history: %v", err)
			}
		}
	}
}

// load reads both histories: the hourly one first (the year's is built from it when missing).
func (k *Keeper) load(ctx context.Context) error {
	series, err := k.Store.Load(ctx)
	if err != nil {
		return err
	}
	year, err := k.Store.LoadYear(ctx)
	if err != nil {
		return err
	}
	k.Recorder.Replace(series)
	k.Recorder.ReplaceYear(year)
	return nil
}

func (k *Keeper) save(ctx context.Context) error {
	now := time.Now()
	if err := k.Store.Save(ctx, k.Recorder.Snapshot(now)); err != nil {
		return err
	}
	return k.Store.SaveYear(ctx, k.Recorder.SnapshotYear(now))
}
