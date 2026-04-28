package housekeeper

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/executor"
	"github.com/feza-ai/spark/internal/manifest"
	"github.com/feza-ai/spark/internal/state"
)

// fakeStore implements the housekeeper's store interface backed by a
// map. It's intentionally tiny; we don't want to take a dependency on
// state.PodStore's full mutation surface here.
type fakeStore struct {
	mu      sync.Mutex
	records map[string]state.PodRecord
	deleted []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[string]state.PodRecord)}
}

func (f *fakeStore) put(rec state.PodRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[rec.Spec.Name] = rec
}

func (f *fakeStore) List(status state.PodStatus) []state.PodRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]state.PodRecord, 0, len(f.records))
	for _, r := range f.records {
		if status != "" && r.Status != status {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (f *fakeStore) Delete(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.records[name]; !ok {
		return false
	}
	delete(f.records, name)
	f.deleted = append(f.deleted, name)
	return true
}

// fakeExec implements the housekeeper's reaper interface.
type fakeExec struct {
	mu          sync.Mutex
	pods        []executor.PodListEntry
	listErr     error
	removed     []string
	removeErr   error
	pruneCount  int
	pruneErr    error
	pruneCalls  int
}

func (f *fakeExec) ListPods(_ context.Context) ([]executor.PodListEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	cp := make([]executor.PodListEntry, len(f.pods))
	copy(cp, f.pods)
	return cp, nil
}

func (f *fakeExec) RemovePod(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, name)
	// Drop from podman list as well so subsequent passes don't see it.
	out := f.pods[:0]
	for _, p := range f.pods {
		if p.Name != name {
			out = append(out, p)
		}
	}
	f.pods = out
	return nil
}

func (f *fakeExec) PruneImages(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneCalls++
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	return f.pruneCount, nil
}

func newRec(name string, status state.PodStatus, finishedAt time.Time, ann map[string]string) state.PodRecord {
	return state.PodRecord{
		Spec:       manifest.PodSpec{Name: name, Annotations: ann},
		Status:     status,
		FinishedAt: finishedAt,
	}
}

func TestReapTTL(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		cfg         Config
		records     []state.PodRecord
		now         time.Time
		wantDeleted []string
		wantCounts  map[string]int64
	}{
		{
			name: "completed pod aged past TTL is reaped",
			cfg:  Config{CompletedPodTTL: time.Hour, FailedPodTTL: 24 * time.Hour},
			records: []state.PodRecord{
				newRec("c1", state.StatusCompleted, base.Add(-2*time.Hour), nil),
			},
			now:         base,
			wantDeleted: []string{"c1"},
			wantCounts:  map[string]int64{"ttl_completed": 1},
		},
		{
			name: "completed pod within TTL is kept",
			cfg:  Config{CompletedPodTTL: time.Hour, FailedPodTTL: 24 * time.Hour},
			records: []state.PodRecord{
				newRec("c1", state.StatusCompleted, base.Add(-30*time.Minute), nil),
			},
			now:         base,
			wantDeleted: nil,
		},
		{
			name: "failed pod aged past failed TTL is reaped",
			cfg:  Config{CompletedPodTTL: time.Hour, FailedPodTTL: 24 * time.Hour},
			records: []state.PodRecord{
				newRec("f1", state.StatusFailed, base.Add(-25*time.Hour), nil),
			},
			now:         base,
			wantDeleted: []string{"f1"},
			wantCounts:  map[string]int64{"ttl_failed": 1},
		},
		{
			name: "failed pod within failed TTL is kept (uses failed not completed TTL)",
			cfg:  Config{CompletedPodTTL: time.Hour, FailedPodTTL: 24 * time.Hour},
			records: []state.PodRecord{
				newRec("f1", state.StatusFailed, base.Add(-2*time.Hour), nil),
			},
			now:         base,
			wantDeleted: nil,
		},
		{
			name: "annotation override shortens TTL",
			cfg:  Config{CompletedPodTTL: 24 * time.Hour},
			records: []state.PodRecord{
				newRec("a1", state.StatusCompleted, base.Add(-10*time.Minute),
					map[string]string{TTLAnnotation: "5m"}),
			},
			now:         base,
			wantDeleted: []string{"a1"},
			wantCounts:  map[string]int64{"ttl_completed": 1},
		},
		{
			name: "annotation override lengthens TTL",
			cfg:  Config{CompletedPodTTL: time.Minute},
			records: []state.PodRecord{
				newRec("a1", state.StatusCompleted, base.Add(-10*time.Minute),
					map[string]string{TTLAnnotation: "1h"}),
			},
			now:         base,
			wantDeleted: nil,
		},
		{
			name: "annotation TTL of zero disables for that pod",
			cfg:  Config{CompletedPodTTL: time.Minute},
			records: []state.PodRecord{
				newRec("a1", state.StatusCompleted, base.Add(-10*time.Minute),
					map[string]string{TTLAnnotation: "0s"}),
			},
			now:         base,
			wantDeleted: nil,
		},
		{
			name: "config TTL of zero disables completed cleanup",
			cfg:  Config{CompletedPodTTL: 0, FailedPodTTL: time.Hour},
			records: []state.PodRecord{
				newRec("c1", state.StatusCompleted, base.Add(-100*time.Hour), nil),
			},
			now:         base,
			wantDeleted: nil,
		},
		{
			name: "running pods are never reaped",
			cfg:  Config{CompletedPodTTL: time.Minute, FailedPodTTL: time.Minute},
			records: []state.PodRecord{
				{Spec: manifest.PodSpec{Name: "r1"}, Status: state.StatusRunning},
			},
			now:         base,
			wantDeleted: nil,
		},
		{
			name: "invalid annotation falls back to default TTL",
			cfg:  Config{CompletedPodTTL: time.Minute},
			records: []state.PodRecord{
				newRec("a1", state.StatusCompleted, base.Add(-10*time.Minute),
					map[string]string{TTLAnnotation: "not-a-duration"}),
			},
			now:         base,
			wantDeleted: []string{"a1"},
			wantCounts:  map[string]int64{"ttl_completed": 1},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFakeStore()
			for _, r := range tc.records {
				s.put(r)
			}
			e := &fakeExec{}
			h, c := New(s, e, tc.cfg)
			h.SetClock(func() time.Time { return tc.now })

			h.RunOnce(context.Background())

			if !equalStringSet(s.deleted, tc.wantDeleted) {
				t.Errorf("deleted=%v want %v", s.deleted, tc.wantDeleted)
			}
			for reason, want := range tc.wantCounts {
				if got := c.PodsReaped(reason); got != want {
					t.Errorf("counter[%s]=%d want %d", reason, got, want)
				}
			}
			if c.LastRunSeconds() != tc.now.Unix() {
				t.Errorf("last run seconds = %d want %d", c.LastRunSeconds(), tc.now.Unix())
			}
		})
	}
}

func TestReapOrphans(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	t.Run("terminal orphan reaped after TTL on second pass", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{
			pods: []executor.PodListEntry{{Name: "ghost", Running: false, Status: "Exited"}},
		}
		h, c := New(s, e, Config{OrphanReapTTL: 30 * time.Minute})

		nowVal := base
		h.SetClock(func() time.Time { return nowVal })

		// Pass 1: observed but within grace.
		h.RunOnce(context.Background())
		if got := c.PodsReaped("orphan"); got != 0 {
			t.Fatalf("orphan count after first pass = %d want 0", got)
		}
		if len(e.removed) != 0 {
			t.Fatalf("orphan removed too soon: %v", e.removed)
		}

		// Pass 2: TTL elapsed.
		nowVal = base.Add(45 * time.Minute)
		h.RunOnce(context.Background())
		if got := c.PodsReaped("orphan"); got != 1 {
			t.Fatalf("orphan count after second pass = %d want 1", got)
		}
		if len(e.removed) != 1 || e.removed[0] != "ghost" {
			t.Fatalf("orphan removed = %v want [ghost]", e.removed)
		}
	})

	t.Run("running orphan is warned not killed", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{
			pods: []executor.PodListEntry{{Name: "alive", Running: true, Status: "Running"}},
		}
		h, _ := New(s, e, Config{OrphanReapTTL: time.Minute})
		h.SetClock(func() time.Time { return base.Add(time.Hour) })
		h.RunOnce(context.Background())
		if len(e.removed) != 0 {
			t.Errorf("running orphan should not be removed, got %v", e.removed)
		}
	})

	t.Run("known pod is not treated as orphan", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		s.put(state.PodRecord{Spec: manifest.PodSpec{Name: "tracked"}, Status: state.StatusRunning})
		e := &fakeExec{
			pods: []executor.PodListEntry{{Name: "tracked", Running: false, Status: "Exited"}},
		}
		h, c := New(s, e, Config{OrphanReapTTL: time.Nanosecond})
		h.SetClock(func() time.Time { return base.Add(time.Hour) })
		// Two passes to ensure even with elapsed TTL we don't remove.
		h.RunOnce(context.Background())
		h.RunOnce(context.Background())
		if c.PodsReaped("orphan") != 0 {
			t.Errorf("known pod incorrectly reaped as orphan")
		}
	})

	t.Run("ttl of zero disables orphan reap", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{
			pods: []executor.PodListEntry{{Name: "ghost", Running: false, Status: "Exited"}},
		}
		h, c := New(s, e, Config{OrphanReapTTL: 0})
		h.SetClock(func() time.Time { return base })
		h.RunOnce(context.Background())
		if c.PodsReaped("orphan") != 0 || len(e.removed) != 0 {
			t.Errorf("orphan reaped despite disabled TTL")
		}
	})

	t.Run("orphan that disappears is forgotten", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{
			pods: []executor.PodListEntry{{Name: "ghost", Running: false, Status: "Exited"}},
		}
		h, _ := New(s, e, Config{OrphanReapTTL: time.Hour})
		nowVal := base
		h.SetClock(func() time.Time { return nowVal })

		h.RunOnce(context.Background())
		if _, ok := h.orphanFirstSeen["ghost"]; !ok {
			t.Fatalf("orphan should be tracked after first sighting")
		}
		// Pod disappears from podman.
		e.pods = nil
		nowVal = base.Add(time.Minute)
		h.RunOnce(context.Background())
		if _, ok := h.orphanFirstSeen["ghost"]; ok {
			t.Errorf("orphan first-seen should be cleared once gone")
		}
	})
}

func TestImagePrune(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	t.Run("prunes on first pass and again after interval", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{pruneCount: 3}
		h, c := New(s, e, Config{ImagePruneInterval: time.Hour})
		nowVal := base
		h.SetClock(func() time.Time { return nowVal })

		h.RunOnce(context.Background())
		if e.pruneCalls != 1 {
			t.Fatalf("first pass pruneCalls=%d want 1", e.pruneCalls)
		}
		if c.ImagesPruned() != 3 {
			t.Errorf("imagesPruned=%d want 3", c.ImagesPruned())
		}

		// Within interval: should not prune again.
		nowVal = base.Add(30 * time.Minute)
		h.RunOnce(context.Background())
		if e.pruneCalls != 1 {
			t.Errorf("prune ran inside interval (calls=%d)", e.pruneCalls)
		}

		// After interval: prunes again.
		nowVal = base.Add(2 * time.Hour)
		h.RunOnce(context.Background())
		if e.pruneCalls != 2 {
			t.Errorf("prune did not run after interval (calls=%d)", e.pruneCalls)
		}
		if c.ImagesPruned() != 6 {
			t.Errorf("imagesPruned=%d want 6", c.ImagesPruned())
		}
	})

	t.Run("interval of zero disables prune", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{pruneCount: 5}
		h, c := New(s, e, Config{ImagePruneInterval: 0})
		h.SetClock(func() time.Time { return base })
		h.RunOnce(context.Background())
		if e.pruneCalls != 0 {
			t.Errorf("prune ran despite disabled interval")
		}
		if c.ImagesPruned() != 0 {
			t.Errorf("imagesPruned=%d want 0", c.ImagesPruned())
		}
	})

	t.Run("prune error does not crash the loop", func(t *testing.T) {
		t.Parallel()
		s := newFakeStore()
		e := &fakeExec{pruneErr: errors.New("podman down")}
		h, c := New(s, e, Config{ImagePruneInterval: time.Minute})
		h.SetClock(func() time.Time { return base })
		h.RunOnce(context.Background())
		if c.ImagesPruned() != 0 {
			t.Errorf("imagesPruned should stay 0 on error, got %d", c.ImagesPruned())
		}
	})
}

func TestAnnotationTTLParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw      string
		wantDur  time.Duration
		wantOk   bool
	}{
		{"1h", time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"0s", 0, true},
		{"-5m", 0, true}, // negative treated as disable
		{"", 0, false},
		{"   ", 0, false},
		{"garbage", 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			d, ok := annotationTTL(map[string]string{TTLAnnotation: tc.raw})
			if d != tc.wantDur || ok != tc.wantOk {
				t.Errorf("annotationTTL(%q) = (%v,%v) want (%v,%v)",
					tc.raw, d, ok, tc.wantDur, tc.wantOk)
			}
		})
	}

	if d, ok := annotationTTL(nil); d != 0 || ok {
		t.Errorf("nil annotations should yield (0,false), got (%v,%v)", d, ok)
	}
	if d, ok := annotationTTL(map[string]string{"unrelated": "1h"}); d != 0 || ok {
		t.Errorf("unrelated annotations should yield (0,false), got (%v,%v)", d, ok)
	}
}

func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
