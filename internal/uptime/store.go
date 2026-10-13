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
	cm, err := s.Client.CoreV1().ConfigMaps(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data := cm.BinaryData[dataKey]
	if len(data) == 0 {
		return nil, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return nil, err
	}
	var series []Series
	return series, json.Unmarshal(raw, &series)
}

// Save writes the history.
func (s *Store) Save(ctx context.Context, series []Series) error {
	raw, err := json.Marshal(series)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	cms := s.Client.CoreV1().ConfigMaps(s.Namespace)
	cm, err := cms.Get(ctx, s.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, Labels: s.Labels},
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
	if series, err := k.Store.Load(ctx); err != nil {
		logger.Printf("Warning: loading the uptime history: %v", err)
	} else {
		k.Recorder.Replace(series)
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
				k.Store.Save(saveCtx, k.Recorder.Snapshot(time.Now()))
			}
			return
		case <-tick.C:
		}
		leading := k.Leading()
		if leading && !wasLeading {
			// Just became the leader: continue from what the previous one saved.
			if series, err := k.Store.Load(ctx); err == nil {
				k.Recorder.Replace(series)
			}
		}
		wasLeading = leading
		if !leading {
			if series, err := k.Store.Load(ctx); err == nil {
				k.Recorder.Replace(series)
			}
			continue
		}
		k.Recorder.Sample(time.Now(), k.Sample())
		if n%5 == 0 && k.Recorder.TakeDirty() {
			if err := k.Store.Save(ctx, k.Recorder.Snapshot(time.Now())); err != nil {
				logger.Printf("Warning: saving the uptime history: %v", err)
			}
		}
	}
}
