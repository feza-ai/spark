package state

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

func demandSpec() manifest.PodSpec {
	return manifest.PodSpec{Name: "model", OnDemand: &manifest.OnDemandSpec{HostPort: 8080, ReadinessPath: "/ready", IdleTimeout: time.Second, StartupTimeout: time.Minute, MaxRequests: 2}}
}

func TestRequestProtectsWorkloadFromIdleEviction(t *testing.T) {
	s := NewPodStore()
	s.Apply(demandSpec())
	p, _ := s.Get("model")
	if p.Status != StatusDormant {
		t.Fatal(p.Status)
	}
	_, release, err := s.AcquireRequest("model")
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateStatus("model", StatusRunning, "started")
	if s.TryIdleStop("model", time.Now().Add(time.Hour)) {
		t.Fatal("evicted active request")
	}
	release()
	release()
	p, _ = s.Get("model")
	if p.InFlight != 0 {
		t.Fatal("release not idempotent")
	}
	if !s.TryIdleStop("model", time.Now().Add(time.Hour)) {
		t.Fatal("idle workload not evicted")
	}
	_, release, err = s.AcquireRequest("model")
	if err != nil {
		t.Fatal(err)
	}
	release()
	p, _ = s.Get("model")
	if p.Status != StatusStopping {
		t.Fatal("request interrupted committed shutdown")
	}
}

func TestRequestLimitAndReplacement(t *testing.T) {
	s := NewPodStore()
	s.Apply(demandSpec())
	_, r1, _ := s.AcquireRequest("model")
	_, r2, _ := s.AcquireRequest("model")
	if _, _, err := s.AcquireRequest("model"); !errors.Is(err, ErrRequestLimit) {
		t.Fatal(err)
	}
	old, _ := s.Get("model")
	s.Delete("model")
	s.Apply(demandSpec())
	r1()
	r2()
	p, _ := s.Get("model")
	if p.InFlight != 0 || p.Generation == old.Generation {
		t.Fatal("old lease affected replacement")
	}
	s.SetReadOnly(true)
	if _, _, err := s.AcquireRequest("model"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestOnDemandPersistsDormancyAndResetsActivity(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "pods.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewPodStore()
	s.Apply(demandSpec())
	s.SetRawManifest("model", []byte("original manifest"))
	p, _ := s.Get("model")
	if err := db.SavePod(&p); err != nil {
		t.Fatal(err)
	}
	records, err := db.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	recovered := NewPodStore()
	recovered.LoadFrom(records)
	p, _ = recovered.Get("model")
	if p.Status != StatusDormant || p.Spec.OnDemand.HostPort != 8080 || string(p.RawManifest) != "original manifest" || p.Ready || p.InFlight != 0 || p.LastActivity.IsZero() {
		t.Fatalf("recovery: %+v", p)
	}
}

func TestConcurrentRequestsAndEviction(t *testing.T) {
	s := NewPodStore()
	s.Apply(demandSpec())
	s.UpdateStatus("model", StatusRunning, "started")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, release, err := s.AcquireRequest("model")
				if err == nil {
					release()
				}
				s.TryIdleStop("model", time.Now())
			}
		}()
	}
	wg.Wait()
	p, _ := s.Get("model")
	if p.InFlight != 0 || p.Status != StatusRunning {
		t.Fatalf("bad activity: %+v", p)
	}
}
