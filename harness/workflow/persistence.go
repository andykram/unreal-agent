package workflow

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrStaleRunRevision = errors.New("workflow checkpoint revision changed; reload before continuing")

const runStoreVersion = 1
const runStoreApplicationID = 0x57524650

// Store records simulator checkpoints, not distributed side-effect leases.
// A successful save is the boundary after which the caller may acknowledge work.
type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(absolute); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("checkpoint path must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	location := url.URL{Scheme: "file", Path: absolute}
	query := location.Query()
	// Reapply connection-local settings if database/sql replaces a connection.
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) initialize() error {
	// Reject unknown formats before changing their journal or vacuum settings.
	var existingVersion, existingApplication int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&existingVersion); err != nil {
		return err
	}
	if err := store.db.QueryRow("PRAGMA application_id").Scan(&existingApplication); err != nil {
		return err
	}
	if existingVersion > runStoreVersion {
		return fmt.Errorf("checkpoint schema version %d is newer than supported %d", existingVersion, runStoreVersion)
	}
	if existingApplication != 0 && existingApplication != runStoreApplicationID {
		return fmt.Errorf("checkpoint database has an unexpected application ID")
	}
	// Setting this before the first table avoids an expensive full VACUUM later.
	if _, err := store.db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		return err
	}
	var journal string
	if err := store.db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return err
	}
	if journal != "wal" {
		return fmt.Errorf("checkpoint database requires WAL, got %q", journal)
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version, applicationID int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := tx.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		return err
	}
	if version > runStoreVersion {
		return fmt.Errorf("checkpoint schema version %d is newer than supported %d", version, runStoreVersion)
	}
	if version == runStoreVersion {
		if applicationID != runStoreApplicationID {
			return fmt.Errorf("checkpoint database has an unexpected application ID")
		}
		return tx.Commit()
	}
	if version != 0 || applicationID != 0 {
		return fmt.Errorf("unrecognized checkpoint database")
	}
	var tables int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return err
	}
	if tables != 0 {
		return fmt.Errorf("refusing to initialize a nonempty unrelated database")
	}
	if _, err := tx.Exec(`CREATE TABLE runs (
 id TEXT PRIMARY KEY,
 graph_json BLOB NOT NULL,
 state_json BLOB NOT NULL,
 revision INTEGER NOT NULL CHECK(revision > 0),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 completed_at INTEGER
 ); CREATE INDEX runs_completed ON runs(completed_at) WHERE completed_at IS NOT NULL;`); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id=%d", runStoreApplicationID)); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", runStoreVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) Close() error { return store.db.Close() }

func CompletedRun(graph Graph, state State) bool {
	if len(graph.Steps) == 0 {
		return false
	}
	for _, step := range graph.Steps {
		status := state[step.ID].Status
		if status != "completed" && status != "skipped" {
			return false
		}
	}
	return true
}

func (store *Store) Create(graph Graph, state State) (string, int64, error) {
	if err := Validate(graph); err != nil {
		return "", 0, err
	}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return "", 0, err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", 0, err
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", 0, err
	}
	id := hex.EncodeToString(entropy[:])
	now := time.Now().UnixNano()
	var completed any
	if CompletedRun(graph, state) {
		completed = now
	}
	_, err = store.db.Exec("INSERT INTO runs(id,graph_json,state_json,revision,created_at,updated_at,completed_at) VALUES(?,?,?,1,?,?,?)", id, graphJSON, stateJSON, now, now, completed)
	if err != nil {
		return "", 0, err
	}
	return id, 1, nil
}

func (store *Store) Load(id string) (Graph, State, int64, error) {
	var graph Graph
	var state State
	var graphJSON, stateJSON []byte
	var revision int64
	if err := store.db.QueryRow("SELECT graph_json,state_json,revision FROM runs WHERE id=?", id).Scan(&graphJSON, &stateJSON, &revision); err != nil {
		return graph, nil, 0, fmt.Errorf("load run %s: %w", id, err)
	}
	// Graph numeric fields retain the existing decoder's float64 representation.
	if err := json.Unmarshal(graphJSON, &graph); err != nil {
		return graph, nil, 0, err
	}
	if err := Validate(graph); err != nil {
		return graph, nil, 0, fmt.Errorf("stored graph: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(stateJSON)))
	decoder.UseNumber()
	if err := decoder.Decode(&state); err != nil {
		return graph, nil, 0, err
	}
	if state == nil {
		state = State{}
	}
	return graph, state, revision, nil
}

func (store *Store) Save(id string, expectedRevision int64, state State) (int64, error) {
	if expectedRevision <= 0 {
		return 0, ErrStaleRunRevision
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return 0, err
	}
	tx, err := store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var graphJSON []byte
	var revision int64
	if err := tx.QueryRow("SELECT graph_json,revision FROM runs WHERE id=?", id).Scan(&graphJSON, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrStaleRunRevision
		}
		return 0, err
	}
	if revision != expectedRevision {
		return 0, ErrStaleRunRevision
	}
	var graph Graph
	if err := json.Unmarshal(graphJSON, &graph); err != nil {
		return 0, err
	}
	now := time.Now().UnixNano()
	var completed any
	if CompletedRun(graph, state) {
		completed = now
	}
	// Re-saving completed state retains its original retention deadline. Resetting
	// to nonterminal clears it, and never resets the monotonic revision counter.
	result, err := tx.Exec(`UPDATE runs SET state_json=?,revision=revision+1,updated_at=?,
 completed_at=CASE WHEN ? IS NULL THEN NULL ELSE COALESCE(completed_at,?) END
 WHERE id=? AND revision=?`, stateJSON, now, completed, completed, id, expectedRevision)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, ErrStaleRunRevision
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision + 1, nil
}

// cleanup deletes only successfully terminal runs. Failed, blocked, waiting, and
// active runs have no completed_at. Exclusions protect the caller's resume target.
func (store *Store) Cleanup(retention time.Duration, batch int, excludeIDs ...string) (int64, error) {
	if retention <= 0 || batch <= 0 || batch > 1000 {
		return 0, fmt.Errorf("cleanup requires positive retention and batch size 1..1000")
	}
	args := []any{time.Now().Add(-retention).UnixNano()}
	exclusions := ""
	if len(excludeIDs) > 0 {
		marks := make([]string, len(excludeIDs))
		for index, id := range excludeIDs {
			marks[index] = "?"
			args = append(args, id)
		}
		exclusions = " AND id NOT IN (" + strings.Join(marks, ",") + ")"
	}
	args = append(args, batch)
	result, err := store.db.Exec("DELETE FROM runs WHERE id IN (SELECT id FROM runs WHERE completed_at IS NOT NULL AND completed_at < ?"+exclusions+" ORDER BY completed_at,id LIMIT ?)", args...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	// Vacuum reclaims at most 64 pages; the passive checkpoint never waits for readers.
	// Deletion above was one atomic statement.
	if _, err := store.db.Exec("PRAGMA incremental_vacuum(64)"); err != nil {
		return deleted, err
	}
	var busy, logFrames, checkpointed int
	if err := store.db.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return deleted, err
	}
	return deleted, nil
}
