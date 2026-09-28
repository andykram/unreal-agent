package workflow

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRetainsActiveRunsAndOriginalDeadline(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "runs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	graph := Graph{Version: 1, Name: "cleanup", Steps: []Step{{ID: "step", Kind: "approval"}}}
	create := func(status string, age time.Duration) (string, int64) {
		t.Helper()
		id, revision, err := store.Create(graph, State{"step": {Status: status}})
		if err != nil {
			t.Fatal(err)
		}
		if age > 0 {
			_, err = store.db.Exec("UPDATE runs SET completed_at=? WHERE id=?", time.Now().Add(-age).UnixNano(), id)
			if err != nil {
				t.Fatal(err)
			}
		}
		return id, revision
	}
	excluded, revision := create("completed", 4*time.Hour)
	first, _ := create("skipped", 3*time.Hour)
	second, _ := create("completed", 2*time.Hour)
	fresh, _ := create("completed", 30*time.Minute)
	partialGraph := Graph{Version: 1, Name: "partial", Steps: []Step{
		{ID: "first", Kind: "approval"},
		{ID: "second", Kind: "approval"},
	}}
	createOldPartial := func(firstStatus, secondStatus string) string {
		t.Helper()
		id, _, err := store.Create(partialGraph, State{
			"first":  {Status: firstStatus},
			"second": {Status: secondStatus},
		})
		if err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-4 * time.Hour).UnixNano()
		if _, err := store.db.Exec("UPDATE runs SET created_at=?,updated_at=? WHERE id=?", old, old, id); err != nil {
			t.Fatal(err)
		}
		var completed sql.NullInt64
		if err := store.db.QueryRow("SELECT completed_at FROM runs WHERE id=?", id).Scan(&completed); err != nil || completed.Valid {
			t.Fatalf("partial run has completion deadline: %v, %v", completed, err)
		}
		return id
	}
	running := createOldPartial("completed", "running")
	failed := createOldPartial("skipped", "failed")
	var deadline int64
	if err := store.db.QueryRow("SELECT completed_at FROM runs WHERE id=?", excluded).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(excluded, revision, State{"step": {Status: "completed"}}); err != nil {
		t.Fatal(err)
	}
	var savedDeadline int64
	if err := store.db.QueryRow("SELECT completed_at FROM runs WHERE id=?", excluded).Scan(&savedDeadline); err != nil || savedDeadline != deadline {
		t.Fatalf("completed deadline changed: %d -> %d: %v", deadline, savedDeadline, err)
	}
	for _, want := range []int64{1, 1, 0} {
		deleted, err := store.Cleanup(time.Hour, 1, excluded)
		if err != nil || deleted != want {
			t.Fatalf("deleted %d, want %d: %v", deleted, want, err)
		}
	}
	for _, id := range []string{first, second} {
		if _, _, _, err := store.Load(id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expired run %s survived: %v", id, err)
		}
	}
	for _, id := range []string{excluded, fresh, running, failed} {
		if _, _, _, err := store.Load(id); err != nil {
			t.Fatalf("retained run %s missing: %v", id, err)
		}
	}
}
