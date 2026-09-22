package main

import (
	"errors"
	"testing"
)

// TestHostMemoryAdapter_ReadsFreshEveryCall pins the reason this adapter
// does not cache the way hostLoadAdapter does: memory can collapse between
// admissions, so each call must see the host as it is now.
func TestHostMemoryAdapter_ReadsFreshEveryCall(t *testing.T) {
	t.Parallel()

	readings := []int{60000, 12000, 500}
	i := 0
	h := &hostMemoryAdapter{read: func() (int, error) {
		v := readings[i]
		i++
		return v, nil
	}}

	for n, want := range readings {
		got, ok := h.AvailableMemoryMB()
		if !ok {
			t.Fatalf("call %d: ok = false, want true", n)
		}
		if got != want {
			t.Fatalf("call %d: AvailableMemoryMB() = %d, want %d", n, got, want)
		}
	}
}

// TestHostMemoryAdapter_ReadFailureReportsNoReading covers the degradation
// path on a host without /proc/meminfo. A failed read must report ok=false
// so the guard is skipped, never a zero that would refuse every pod.
func TestHostMemoryAdapter_ReadFailureReportsNoReading(t *testing.T) {
	t.Parallel()

	h := &hostMemoryAdapter{read: func() (int, error) {
		return 0, errors.New("meminfo not available")
	}}

	for n := 0; n < 3; n++ {
		mb, ok := h.AvailableMemoryMB()
		if ok {
			t.Fatalf("call %d: ok = true, want false", n)
		}
		if mb != 0 {
			t.Fatalf("call %d: AvailableMemoryMB() = %d, want 0", n, mb)
		}
	}
	if !h.warned {
		t.Fatal("expected the first read failure to be recorded so it logs once, not once per admission")
	}
}

// TestHostMemoryAdapter_RecoveryReWarns checks that a host which starts
// working again, then fails again, logs the second outage rather than
// staying silent because it warned about the first one.
func TestHostMemoryAdapter_RecoveryReWarns(t *testing.T) {
	t.Parallel()

	fail := true
	h := &hostMemoryAdapter{read: func() (int, error) {
		if fail {
			return 0, errors.New("meminfo not available")
		}
		return 4096, nil
	}}

	if _, ok := h.AvailableMemoryMB(); ok {
		t.Fatal("expected the first read to fail")
	}
	if !h.warned {
		t.Fatal("expected warned to be set after a failure")
	}

	fail = false
	if _, ok := h.AvailableMemoryMB(); !ok {
		t.Fatal("expected the recovered read to succeed")
	}
	if h.warned {
		t.Fatal("expected a successful read to clear the warned flag so the next outage is logged")
	}
}
