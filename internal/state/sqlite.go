package state

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/feza-ai/spark/internal/manifest"
	_ "modernc.org/sqlite"
)

// SQLiteStore persists pod state to a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens a SQLite database at the given path and initialises the schema.
func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}

	const createPods = `CREATE TABLE IF NOT EXISTS pods (
		name TEXT PRIMARY KEY,
		spec_json TEXT NOT NULL,
		status TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		restarts INTEGER DEFAULT 0,
		retry_count INTEGER DEFAULT 0
	)`
	if _, err := db.Exec(createPods); err != nil {
		db.Close()
		return nil, err
	}

	const createEvents = `CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		pod_name TEXT NOT NULL REFERENCES pods(name) ON DELETE CASCADE,
		time TEXT NOT NULL,
		type TEXT NOT NULL,
		message TEXT
	)`
	if _, err := db.Exec(createEvents); err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

// SavePod upserts a pod record.
func (s *SQLiteStore) SavePod(rec *PodRecord) error {
	specJSON, err := json.Marshal(rec.Spec)
	if err != nil {
		return err
	}

	var startedAt, finishedAt *string
	if !rec.StartedAt.IsZero() {
		v := rec.StartedAt.Format(time.RFC3339)
		startedAt = &v
	}
	if !rec.FinishedAt.IsZero() {
		v := rec.FinishedAt.Format(time.RFC3339)
		finishedAt = &v
	}

	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO pods (name, spec_json, status, started_at, finished_at, restarts, retry_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.Spec.Name, string(specJSON), string(rec.Status),
		startedAt, finishedAt, rec.Restarts, rec.RetryCount,
	)
	return err
}

// SaveEvent inserts an event for a pod.
func (s *SQLiteStore) SaveEvent(podName string, event PodEvent) error {
	_, err := s.db.Exec(
		`INSERT INTO events (pod_name, time, type, message) VALUES (?, ?, ?, ?)`,
		podName, event.Time.Format(time.RFC3339), event.Type, event.Message,
	)
	return err
}

// LoadAll reads all pods and their events from the database.
func (s *SQLiteStore) LoadAll() (map[string]*PodRecord, error) {
	rows, err := s.db.Query(
		`SELECT name, spec_json, status, started_at, finished_at, restarts, retry_count FROM pods`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pods := make(map[string]*PodRecord)
	for rows.Next() {
		var name, specJSON, status string
		var startedAt, finishedAt sql.NullString
		var restarts, retryCount int

		if err := rows.Scan(&name, &specJSON, &status, &startedAt, &finishedAt, &restarts, &retryCount); err != nil {
			return nil, err
		}

		var spec manifest.PodSpec
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			return nil, err
		}

		rec := &PodRecord{
			Spec:       spec,
			Status:     PodStatus(status),
			Restarts:   restarts,
			RetryCount: retryCount,
		}
		if startedAt.Valid {
			if t, err := time.Parse(time.RFC3339, startedAt.String); err == nil {
				rec.StartedAt = t
			}
		}
		if finishedAt.Valid {
			if t, err := time.Parse(time.RFC3339, finishedAt.String); err == nil {
				rec.FinishedAt = t
			}
		}
		pods[name] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load events
	evtRows, err := s.db.Query(`SELECT pod_name, time, type, message FROM events ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer evtRows.Close()

	for evtRows.Next() {
		var podName, timeStr, typ string
		var message sql.NullString
		if err := evtRows.Scan(&podName, &timeStr, &typ, &message); err != nil {
			return nil, err
		}
		rec, ok := pods[podName]
		if !ok {
			continue
		}
		t, _ := time.Parse(time.RFC3339, timeStr)
		rec.Events = append(rec.Events, PodEvent{
			Time:    t,
			Type:    typ,
			Message: message.String,
		})
	}
	return pods, evtRows.Err()
}

// DeletePod removes a pod and its events (via CASCADE).
func (s *SQLiteStore) DeletePod(name string) error {
	_, err := s.db.Exec(`DELETE FROM pods WHERE name = ?`, name)
	return err
}

// PruneBefore deletes completed or failed pods with finished_at before cutoff.
// Returns the number of pods deleted.
func (s *SQLiteStore) PruneBefore(cutoff time.Time) (int, error) {
	result, err := s.db.Exec(
		`DELETE FROM pods WHERE status IN ('completed', 'failed') AND finished_at IS NOT NULL AND finished_at < ?`,
		cutoff.Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
