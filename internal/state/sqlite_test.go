package state

import (
	"testing"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
)

func TestOpenSQLite(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	// Verify tables exist
	for _, table := range []string{"pods", "events"} {
		var name string
		err := store.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestSavePodAndLoadAll(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	started := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
	finished := time.Date(2026, 3, 19, 10, 5, 0, 0, time.UTC)

	rec := &PodRecord{
		Spec: manifest.PodSpec{
			Name:       "test-pod",
			SourceKind: "Job",
			SourceName: "test-job",
			Containers: []manifest.ContainerSpec{
				{Name: "main", Image: "alpine:latest", Command: []string{"echo", "hello"}},
			},
			Labels:      map[string]string{"app": "test"},
			Annotations: map[string]string{"note": "value"},
		},
		Status:     StatusCompleted,
		StartedAt:  started,
		FinishedAt: finished,
		Restarts:   2,
		RetryCount: 1,
	}

	if err := store.SavePod(rec); err != nil {
		t.Fatalf("SavePod: %v", err)
	}

	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods))
	}

	got := pods["test-pod"]
	if got.Spec.Name != "test-pod" {
		t.Errorf("spec.Name = %q, want %q", got.Spec.Name, "test-pod")
	}
	if got.Spec.SourceKind != "Job" {
		t.Errorf("spec.SourceKind = %q, want %q", got.Spec.SourceKind, "Job")
	}
	if len(got.Spec.Containers) != 1 || got.Spec.Containers[0].Image != "alpine:latest" {
		t.Errorf("spec.Containers mismatch")
	}
	if got.Spec.Labels["app"] != "test" {
		t.Errorf("spec.Labels mismatch")
	}
	if got.Status != StatusCompleted {
		t.Errorf("status = %q, want %q", got.Status, StatusCompleted)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}
	if !got.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, finished)
	}
	if got.Restarts != 2 {
		t.Errorf("Restarts = %d, want 2", got.Restarts)
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
}

func TestSaveEvent(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	rec := &PodRecord{
		Spec:   manifest.PodSpec{Name: "evt-pod"},
		Status: StatusRunning,
	}
	if err := store.SavePod(rec); err != nil {
		t.Fatalf("SavePod: %v", err)
	}

	t1 := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 19, 10, 1, 0, 0, time.UTC)

	events := []PodEvent{
		{Time: t1, Type: "scheduled", Message: "pod scheduled"},
		{Time: t2, Type: "started", Message: "container started"},
	}
	for _, e := range events {
		if err := store.SaveEvent("evt-pod", e); err != nil {
			t.Fatalf("SaveEvent: %v", err)
		}
	}

	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	got := pods["evt-pod"]
	if len(got.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got.Events))
	}
	if got.Events[0].Type != "scheduled" {
		t.Errorf("event[0].Type = %q, want %q", got.Events[0].Type, "scheduled")
	}
	if got.Events[1].Type != "started" {
		t.Errorf("event[1].Type = %q, want %q", got.Events[1].Type, "started")
	}
	if got.Events[0].Message != "pod scheduled" {
		t.Errorf("event[0].Message = %q, want %q", got.Events[0].Message, "pod scheduled")
	}
}

func TestListEvents(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	// Create a pod for events
	if err := store.SavePod(&PodRecord{
		Spec:   manifest.PodSpec{Name: "le-pod"},
		Status: StatusRunning,
	}); err != nil {
		t.Fatalf("SavePod: %v", err)
	}

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	for _, e := range []PodEvent{
		{Time: t1, Type: "scheduled", Message: "first"},
		{Time: t2, Type: "started", Message: "second"},
		{Time: t3, Type: "running", Message: "third"},
	} {
		if err := store.SaveEvent("le-pod", e); err != nil {
			t.Fatalf("SaveEvent: %v", err)
		}
	}

	tests := []struct {
		name      string
		podName   string
		since     time.Time
		wantCount int
		wantFirst string
	}{
		{
			name:      "All",
			podName:   "le-pod",
			since:     time.Time{},
			wantCount: 3,
			wantFirst: "scheduled",
		},
		{
			name:      "Since",
			podName:   "le-pod",
			since:     t2,
			wantCount: 2,
			wantFirst: "started",
		},
		{
			name:      "NoPod",
			podName:   "nonexistent",
			since:     time.Time{},
			wantCount: 0,
			wantFirst: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := store.ListEvents(tt.podName, tt.since)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if events == nil {
				t.Fatal("ListEvents returned nil, want empty slice")
			}
			if len(events) != tt.wantCount {
				t.Fatalf("got %d events, want %d", len(events), tt.wantCount)
			}
			if tt.wantCount > 0 && events[0].Type != tt.wantFirst {
				t.Errorf("first event type = %q, want %q", events[0].Type, tt.wantFirst)
			}
		})
	}
}

func TestDeletePod(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	rec := &PodRecord{
		Spec:   manifest.PodSpec{Name: "del-pod"},
		Status: StatusRunning,
	}
	if err := store.SavePod(rec); err != nil {
		t.Fatalf("SavePod: %v", err)
	}
	if err := store.SaveEvent("del-pod", PodEvent{
		Time: time.Now(), Type: "started", Message: "running",
	}); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}

	if err := store.DeletePod("del-pod"); err != nil {
		t.Fatalf("DeletePod: %v", err)
	}

	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(pods) != 0 {
		t.Errorf("expected 0 pods after delete, got %d", len(pods))
	}

	// Verify events were cascade-deleted
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE pod_name = ?`, "del-pod").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 events after cascade delete, got %d", count)
	}
}

func TestPruneBefore(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)

	// Old completed pod
	if err := store.SavePod(&PodRecord{
		Spec:       manifest.PodSpec{Name: "old-pod"},
		Status:     StatusCompleted,
		FinishedAt: old,
	}); err != nil {
		t.Fatalf("SavePod old: %v", err)
	}

	// Recent running pod
	if err := store.SavePod(&PodRecord{
		Spec:   manifest.PodSpec{Name: "new-pod"},
		Status: StatusRunning,
	}); err != nil {
		t.Fatalf("SavePod new: %v", err)
	}

	// Recent completed pod (should not be pruned)
	if err := store.SavePod(&PodRecord{
		Spec:       manifest.PodSpec{Name: "recent-done"},
		Status:     StatusCompleted,
		FinishedAt: recent,
	}); err != nil {
		t.Fatalf("SavePod recent-done: %v", err)
	}

	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	n, err := store.PruneBefore(cutoff)
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d pods, want 1", n)
	}

	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("expected 2 pods remaining, got %d", len(pods))
	}
	if _, ok := pods["old-pod"]; ok {
		t.Error("old-pod should have been pruned")
	}
}

func TestSourcePathSQLiteRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		sourcePath string
	}{
		{"non-empty path", "/etc/spark/manifests/app.yaml"},
		{"empty path", ""},
		{"path with spaces", "/tmp/my manifests/pod.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenSQLite(":memory:")
			if err != nil {
				t.Fatalf("OpenSQLite: %v", err)
			}
			defer store.Close()

			rec := &PodRecord{
				Spec:       manifest.PodSpec{Name: "sp-pod"},
				Status:     StatusRunning,
				SourcePath: tt.sourcePath,
			}
			if err := store.SavePod(rec); err != nil {
				t.Fatalf("SavePod: %v", err)
			}

			pods, err := store.LoadAll()
			if err != nil {
				t.Fatalf("LoadAll: %v", err)
			}
			got := pods["sp-pod"]
			if got == nil {
				t.Fatal("expected sp-pod in loaded pods")
			}
			if got.SourcePath != tt.sourcePath {
				t.Errorf("SourcePath = %q, want %q", got.SourcePath, tt.sourcePath)
			}
		})
	}
}

// TestSavePodRoundTripsReasonAndStartAttempts verifies the new columns added
// to support surfacing container-start failures (issue #8 / T56.1) survive a
// SavePod/LoadAll cycle.
func TestSavePodRoundTripsReasonAndStartAttempts(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	rec := &PodRecord{
		Spec: manifest.PodSpec{
			Name: "fail-pod",
			Containers: []manifest.ContainerSpec{
				{Name: "main", Image: "alpine:latest"},
			},
		},
		Status:        StatusPending,
		Reason:        "volume \"data\" not found",
		StartAttempts: 3,
	}
	if err := store.SavePod(rec); err != nil {
		t.Fatalf("SavePod: %v", err)
	}

	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := pods["fail-pod"]
	if got == nil {
		t.Fatal("expected fail-pod in loaded pods")
	}
	if got.Reason != rec.Reason {
		t.Errorf("Reason = %q, want %q", got.Reason, rec.Reason)
	}
	if got.StartAttempts != rec.StartAttempts {
		t.Errorf("StartAttempts = %d, want %d", got.StartAttempts, rec.StartAttempts)
	}
}

// TestSavePodRoundTripsLastAttemptAt verifies the last_attempt_at column
// added for retry backoff (T56.2) survives a SavePod/LoadAll cycle.
func TestSavePodRoundTripsLastAttemptAt(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	ts := time.Now().UTC().Truncate(time.Second)
	rec := &PodRecord{
		Spec: manifest.PodSpec{
			Name: "retry-pod",
			Containers: []manifest.ContainerSpec{
				{Name: "main", Image: "alpine:latest"},
			},
		},
		Status:        StatusPending,
		Reason:        "transient",
		StartAttempts: 2,
		LastAttemptAt: ts,
	}
	if err := store.SavePod(rec); err != nil {
		t.Fatalf("SavePod: %v", err)
	}
	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := pods["retry-pod"]
	if got == nil {
		t.Fatal("expected retry-pod in loaded pods")
	}
	if !got.LastAttemptAt.Equal(ts) {
		t.Errorf("LastAttemptAt = %v, want %v", got.LastAttemptAt, ts)
	}
}

// TestEnsureColumnMigratesExistingDatabase verifies that opening a database
// created by an older Spark version (without reason/start_attempts columns)
// triggers the PRAGMA-based migration and that subsequent reads/writes work.
func TestEnsureColumnMigratesExistingDatabase(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	// Simulate an older schema by opening raw and creating the minimal pods
	// table without the new columns.
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite initial: %v", err)
	}
	// Drop the new columns via a fresh table to mimic an older install.
	if _, err := store.db.Exec(`DROP TABLE pods`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TABLE pods (
		name TEXT PRIMARY KEY,
		spec_json TEXT NOT NULL,
		status TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		restarts INTEGER DEFAULT 0,
		retry_count INTEGER DEFAULT 0,
		source_path TEXT DEFAULT ''
	)`); err != nil {
		t.Fatalf("recreate legacy schema: %v", err)
	}
	store.Close()

	// Re-open: OpenSQLite should add the missing columns.
	store2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite reopen: %v", err)
	}
	defer store2.Close()

	rec := &PodRecord{
		Spec:          manifest.PodSpec{Name: "migrated"},
		Status:        StatusPending,
		Reason:        "migrated-reason",
		StartAttempts: 5,
	}
	if err := store2.SavePod(rec); err != nil {
		t.Fatalf("SavePod after migration: %v", err)
	}
	pods, err := store2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll after migration: %v", err)
	}
	got := pods["migrated"]
	if got == nil || got.Reason != "migrated-reason" || got.StartAttempts != 5 {
		t.Fatalf("migrated pod round-trip failed: %#v", got)
	}
}

// TestSavePod_RoundtripsAssignedCores verifies AssignedCores survives a
// SavePod/LoadAll cycle and preserves ordering.
func TestSavePod_RoundtripsAssignedCores(t *testing.T) {
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer store.Close()

	rec := &PodRecord{
		Spec: manifest.PodSpec{
			Name:       "pinned",
			SourceKind: "Pod",
			Containers: []manifest.ContainerSpec{
				{Name: "main", Image: "alpine:latest"},
			},
		},
		Status:        StatusRunning,
		AssignedCores: []int{2, 3, 4},
	}

	if err := store.SavePod(rec); err != nil {
		t.Fatalf("SavePod: %v", err)
	}

	pods, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := pods["pinned"]
	if got == nil {
		t.Fatal("pod not loaded")
	}
	if len(got.AssignedCores) != 3 ||
		got.AssignedCores[0] != 2 ||
		got.AssignedCores[1] != 3 ||
		got.AssignedCores[2] != 4 {
		t.Fatalf("AssignedCores round-trip: got %v, want [2 3 4]", got.AssignedCores)
	}
}

// TestSavePod_EmptyAssignedCores verifies nil/empty cores load back as nil.
func TestSavePod_EmptyAssignedCores(t *testing.T) {
	cases := []struct {
		name  string
		input []int
	}{
		{"nil", nil},
		{"empty", []int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenSQLite(":memory:")
			if err != nil {
				t.Fatalf("OpenSQLite: %v", err)
			}
			defer store.Close()

			rec := &PodRecord{
				Spec: manifest.PodSpec{
					Name:       "no-pin-" + tc.name,
					SourceKind: "Pod",
					Containers: []manifest.ContainerSpec{
						{Name: "main", Image: "alpine:latest"},
					},
				},
				Status:        StatusRunning,
				AssignedCores: tc.input,
			}
			if err := store.SavePod(rec); err != nil {
				t.Fatalf("SavePod: %v", err)
			}

			pods, err := store.LoadAll()
			if err != nil {
				t.Fatalf("LoadAll: %v", err)
			}
			got := pods["no-pin-"+tc.name]
			if got == nil {
				t.Fatal("pod not loaded")
			}
			if got.AssignedCores != nil {
				t.Fatalf("AssignedCores: got %v, want nil", got.AssignedCores)
			}
		})
	}
}

// TestMigration_AddsAssignedCoresIdempotent verifies that re-opening a
// database is a safe no-op -- the assigned_cores column is created once
// and subsequent OpenSQLite calls do not error or double-add the column.
func TestMigration_AddsAssignedCoresIdempotent(t *testing.T) {
	path := t.TempDir() + "/cores.db"

	// First open: column is created as part of the CREATE TABLE and
	// ensureColumn is a no-op.
	store1, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite first: %v", err)
	}
	store1.Close()

	// Second open: ensureColumn observes the column and returns cleanly.
	store2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite reopen: %v", err)
	}
	defer store2.Close()

	// The column must exist exactly once.
	rows, err := store2.db.Query("PRAGMA table_info(pods)")
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "assigned_cores" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("assigned_cores column count = %d, want 1", count)
	}
}

// TestMigration_AddsAssignedCoresFromLegacySchema verifies a database created
// by an older Spark version (missing assigned_cores entirely) picks up the
// column via ensureColumn without data loss.
func TestMigration_AddsAssignedCoresFromLegacySchema(t *testing.T) {
	path := t.TempDir() + "/legacy-cores.db"

	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite initial: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE pods`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// Recreate without assigned_cores to simulate a pre-T2.3 install.
	if _, err := store.db.Exec(`CREATE TABLE pods (
		name TEXT PRIMARY KEY,
		spec_json TEXT NOT NULL,
		status TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		restarts INTEGER DEFAULT 0,
		retry_count INTEGER DEFAULT 0,
		source_path TEXT DEFAULT '',
		reason TEXT DEFAULT '',
		start_attempts INTEGER DEFAULT 0,
		last_attempt_at TEXT
	)`); err != nil {
		t.Fatalf("recreate legacy schema: %v", err)
	}
	store.Close()

	store2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite reopen: %v", err)
	}
	defer store2.Close()

	rec := &PodRecord{
		Spec:          manifest.PodSpec{Name: "upgraded"},
		Status:        StatusRunning,
		AssignedCores: []int{0, 1, 2},
	}
	if err := store2.SavePod(rec); err != nil {
		t.Fatalf("SavePod after migration: %v", err)
	}
	pods, err := store2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll after migration: %v", err)
	}
	got := pods["upgraded"]
	if got == nil {
		t.Fatal("pod not loaded after migration")
	}
	if len(got.AssignedCores) != 3 {
		t.Fatalf("AssignedCores after legacy migration: got %v, want [0 1 2]", got.AssignedCores)
	}
}

func TestEncodeDecodeCores(t *testing.T) {
	cases := []struct {
		name    string
		cores   []int
		encoded string
	}{
		{"nil", nil, ""},
		{"empty", []int{}, ""},
		{"single", []int{5}, "5"},
		{"contiguous", []int{2, 3, 4}, "2,3,4"},
		{"sparse", []int{0, 4, 7}, "0,4,7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeCores(tc.cores); got != tc.encoded {
				t.Errorf("encodeCores(%v) = %q, want %q", tc.cores, got, tc.encoded)
			}
			got := decodeCores(tc.encoded)
			if len(tc.cores) == 0 {
				if got != nil {
					t.Errorf("decodeCores(%q) = %v, want nil", tc.encoded, got)
				}
				return
			}
			if len(got) != len(tc.cores) {
				t.Fatalf("decodeCores(%q) = %v, want %v", tc.encoded, got, tc.cores)
			}
			for i := range got {
				if got[i] != tc.cores[i] {
					t.Errorf("decodeCores(%q)[%d] = %d, want %d", tc.encoded, i, got[i], tc.cores[i])
				}
			}
		})
	}
}
