package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

type fakeHostMemory struct {
	mb int
	ok bool
}

func (f fakeHostMemory) AvailableMemoryMB() (int, bool) { return f.mb, f.ok }

// undeclaredPodSpec builds a pod whose container is marked as having
// declared no memory, carrying whatever request the parser's default
// supplied. It mirrors what Parse produces under
// WithDefaultMemoryRequestMB.
func undeclaredPodSpec(name string, priority, cpu, defaultedMem int) manifest.PodSpec {
	spec := podSpec(name, priority, cpu, defaultedMem, 0)
	spec.Containers[0].Resources.MemoryRequestUndeclared = true
	return spec
}

// TestSchedule_LiveMemoryGuard_Boundaries pins the refuse/admit edge
// exactly. The guard refuses when real free memory minus the request would
// land below the reserve, and admits when it lands on or above it.
func TestSchedule_LiveMemoryGuard_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		hostFreeMB  int
		hostOK      bool
		reserveMB   int
		requestMB   int
		wantAction  ScheduleAction
		wantRefusal bool
	}{
		{
			name:        "lands exactly on the reserve: admitted",
			hostFreeMB:  10240,
			hostOK:      true,
			reserveMB:   4096,
			requestMB:   6144,
			wantAction:  Scheduled,
			wantRefusal: false,
		},
		{
			name:        "one MB below the reserve: refused",
			hostFreeMB:  10240,
			hostOK:      true,
			reserveMB:   4096,
			requestMB:   6145,
			wantAction:  Pending,
			wantRefusal: true,
		},
		{
			name:        "comfortably above the reserve: admitted",
			hostFreeMB:  60000,
			hostOK:      true,
			reserveMB:   4096,
			requestMB:   1024,
			wantAction:  Scheduled,
			wantRefusal: false,
		},
		{
			name:        "host already below the reserve refuses even a tiny request",
			hostFreeMB:  1000,
			hostOK:      true,
			reserveMB:   4096,
			requestMB:   1,
			wantAction:  Pending,
			wantRefusal: true,
		},
		{
			name:        "no reading available never refuses: an unreadable host is not an exhausted one",
			hostFreeMB:  0,
			hostOK:      false,
			reserveMB:   4096,
			requestMB:   60000,
			wantAction:  Scheduled,
			wantRefusal: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The ledger is deliberately roomy in every case, so the only
			// thing that can refuse a pod here is the live guard.
			tracker := newTracker(64000, 131072, 0)
			s := NewScheduler(tracker)
			s.SetHostMemory(fakeHostMemory{mb: tt.hostFreeMB, ok: tt.hostOK}, tt.reserveMB)

			result := s.Schedule(podSpec("pod", 100, 1000, tt.requestMB, 0))

			if result.Action != tt.wantAction {
				t.Fatalf("Action = %d, want %d (reason: %s)", result.Action, tt.wantAction, result.Reason)
			}
			if got := errors.Is(result.Err, ErrLiveMemoryExhausted); got != tt.wantRefusal {
				t.Fatalf("errors.Is(Err, ErrLiveMemoryExhausted) = %v, want %v (err: %v)", got, tt.wantRefusal, result.Err)
			}
			if tt.wantRefusal {
				if _, held := tracker.AllocatedBy("pod"); held {
					t.Fatal("a refused pod must hold no allocation")
				}
				if got := s.LiveMemoryRefusals(); got != 1 {
					t.Fatalf("LiveMemoryRefusals() = %d, want 1", got)
				}
			} else if got := s.LiveMemoryRefusals(); got != 0 {
				t.Fatalf("LiveMemoryRefusals() = %d, want 0", got)
			}
		})
	}
}

// TestSchedule_LiveMemoryGuard_ReproducesIssue121 is the regression test
// for the incident: eight containers that declared no memory sat resident
// on a 114372MB node, the declared-request ledger therefore read the node
// as completely empty, and a 100GiB render was admitted against that
// figure. Minutes later the host stopped servicing the network.
//
// Here the ledger is likewise empty (the resident pods declared nothing),
// and real host memory is nearly gone. The 100GiB request must be refused,
// and refused with the named error so the event says "live memory" rather
// than a generic no-fit.
func TestSchedule_LiveMemoryGuard_ReproducesIssue121(t *testing.T) {
	t.Parallel()

	const (
		allocatableMB = 114372
		renderMB      = 102400 // 100GiB
		reserveMB     = 4096
	)

	tracker := newTracker(18000, allocatableMB, 0)
	s := NewScheduler(tracker)

	// Eight resident containers that declared no memory. With defaulting
	// off -- the state the incident ran in -- they charge the ledger
	// nothing, exactly as observed.
	for i := 0; i < 8; i++ {
		name := "builder-" + string(rune('a'+i))
		res := manifest.ResourceList{CPUMillis: 500}
		if err := tracker.Allocate(name, res); err != nil {
			t.Fatalf("allocate %s: %v", name, err)
		}
		s.AddPod(PodInfo{Name: name, Priority: 1000, Resources: res, StartTime: time.Now()})
	}

	// The ledger's own view: the node looks empty.
	if got := tracker.Available().MemoryMB; got != allocatableMB {
		t.Fatalf("precondition: ledger should read the node as empty, available memory = %d, want %d", got, allocatableMB)
	}
	if !tracker.CanFit(manifest.ResourceList{MemoryMB: renderMB}) {
		t.Fatal("precondition: the ledger must approve the render, or this test is not reproducing issue #121")
	}

	// The host's own view: those eight builders are really using the node.
	s.SetHostMemory(fakeHostMemory{mb: 11972, ok: true}, reserveMB)

	result := s.Schedule(podSpec("render", 100, 12000, renderMB, 0))

	if result.Action == Scheduled {
		t.Fatalf("a 100GiB request was admitted onto a host with 11972MB actually free: %s", result.Reason)
	}
	if !errors.Is(result.Err, ErrLiveMemoryExhausted) {
		t.Fatalf("Err = %v, want ErrLiveMemoryExhausted", result.Err)
	}
	if _, held := tracker.AllocatedBy("render"); held {
		t.Fatal("a refused pod must hold no allocation")
	}
}

// TestSchedule_LiveMemoryGuard_NeverAdmitsWhatTheLedgerRefuses is the
// safety property the guard must not break. ADR 013 gave CPU a live-load
// bypass and deliberately withheld one from memory; adding a live memory
// source must add refusals only. Here the host reports abundant free
// memory while the ledger is full -- the pod must still stay Pending.
func TestSchedule_LiveMemoryGuard_NeverAdmitsWhatTheLedgerRefuses(t *testing.T) {
	t.Parallel()

	tracker := newTracker(4000, 1024, 0)
	if err := tracker.Allocate("holder", manifest.ResourceList{CPUMillis: 500, MemoryMB: 900}); err != nil {
		t.Fatalf("allocate holder: %v", err)
	}
	s := NewScheduler(tracker)
	s.AddPod(PodInfo{Name: "holder", Priority: 1000, Resources: manifest.ResourceList{CPUMillis: 500, MemoryMB: 900}, StartTime: time.Now()})
	// The host claims 128GB free. The ledger says 124MB.
	s.SetHostMemory(fakeHostMemory{mb: 131072, ok: true}, 4096)

	result := s.Schedule(podSpec("memory-heavy", 1000, 500, 500, 0))

	if result.Action == Scheduled {
		t.Fatalf("live memory headroom must never override the declared ledger: %s", result.Reason)
	}
	if errors.Is(result.Err, ErrLiveMemoryExhausted) {
		t.Fatal("this is an accounted no-fit, not a live-memory refusal; the named error must not be attached")
	}
	if got := s.LiveMemoryRefusals(); got != 0 {
		t.Fatalf("LiveMemoryRefusals() = %d, want 0 -- the guard never ran, the ledger refused first", got)
	}
}

// TestSchedule_LiveMemoryGuard_GuardsTheCPUOvercommitPathToo covers the
// second admission door. Issue #76's utilization-aware path admits past
// the accounted CPU ceiling; it must not become a way around the live
// memory guard.
func TestSchedule_LiveMemoryGuard_GuardsTheCPUOvercommitPathToo(t *testing.T) {
	t.Parallel()

	tracker := newTracker(4000, 131072, 0)
	if err := tracker.Allocate("holder", manifest.ResourceList{CPUMillis: 3800}); err != nil {
		t.Fatalf("allocate holder: %v", err)
	}
	s := NewScheduler(tracker)
	s.AddPod(PodInfo{Name: "holder", Priority: 1000, Resources: manifest.ResourceList{CPUMillis: 3800}, StartTime: time.Now()})
	s.SetHostLoad(fakeHostLoad{millis: 10000, ok: true})
	s.SetHostMemory(fakeHostMemory{mb: 5000, ok: true}, 4096)

	// Accounted CPU is short (200m free, 1000m requested), so this pod can
	// only reach admission through the overcommit path.
	result := s.Schedule(podSpec("job", 1000, 1000, 2048, 0))

	if result.Action == Scheduled {
		t.Fatalf("the CPU overcommit path must not bypass the live memory guard: %s", result.Reason)
	}
	if !errors.Is(result.Err, ErrLiveMemoryExhausted) {
		t.Fatalf("Err = %v, want ErrLiveMemoryExhausted", result.Err)
	}
	if got := s.CPUOvercommitAdmissions(); got != 0 {
		t.Fatalf("CPUOvercommitAdmissions() = %d, want 0", got)
	}
}

// TestSchedule_LiveMemoryGuard_Disabled checks the kill switch: with no
// HostMemorySource, Schedule admits purely on accounting, exactly as it
// did before the guard existed.
func TestSchedule_LiveMemoryGuard_Disabled(t *testing.T) {
	t.Parallel()

	tracker := newTracker(18000, 114372, 0)
	s := NewScheduler(tracker)

	result := s.Schedule(podSpec("render", 100, 12000, 102400, 0))

	if result.Action != Scheduled {
		t.Fatalf("Action = %d, want Scheduled (reason: %s)", result.Action, result.Reason)
	}
	if got := s.LiveMemoryRefusals(); got != 0 {
		t.Fatalf("LiveMemoryRefusals() = %d, want 0", got)
	}
}

// TestSchedule_DefaultedMemoryAdmissions_Counted checks the operator's
// signal that the ledger rests on a default rather than on declarations.
func TestSchedule_DefaultedMemoryAdmissions_Counted(t *testing.T) {
	t.Parallel()

	tracker := newTracker(18000, 114372, 0)
	s := NewScheduler(tracker)

	if result := s.Schedule(undeclaredPodSpec("undeclared", 100, 500, 2048)); result.Action != Scheduled {
		t.Fatalf("Action = %d, want Scheduled (reason: %s)", result.Action, result.Reason)
	}
	if result := s.Schedule(podSpec("declared", 100, 500, 2048, 0)); result.Action != Scheduled {
		t.Fatalf("Action = %d, want Scheduled (reason: %s)", result.Action, result.Reason)
	}

	if got := s.DefaultedMemoryAdmissions(); got != 1 {
		t.Fatalf("DefaultedMemoryAdmissions() = %d, want 1 -- only the undeclared pod counts", got)
	}
}
