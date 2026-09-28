package workflow

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenStoreDoesNotChangeUnrelatedDatabase(t *testing.T) {
	for _, header := range []struct{ version, application int }{
		{0, 0},
		{runStoreVersion, 0},
		{0, runStoreApplicationID},
	} {
		t.Run(fmt.Sprintf("version-%d-application-%d", header.version, header.application), func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "unrelated.sqlite")
			db, err := sql.Open("sqlite", filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE unrelated(value TEXT); INSERT INTO unrelated(value) VALUES('keep')"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d; PRAGMA application_id=%d", header.version, header.application)); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filename, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			if store, err := OpenStore(filename); err == nil || store != nil || !(strings.Contains(err.Error(), "unrelated database") || strings.Contains(err.Error(), "unrecognized checkpoint database")) {
				t.Fatalf("accepted unrelated database: %v", err)
			}
			after, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("unrelated database contents changed")
			}
			info, err := os.Stat(filename)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0644 {
				t.Fatalf("unrelated database permissions changed: %v", info.Mode().Perm())
			}
			db, err = sql.Open("sqlite", filename)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var journal, value string
			if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT value FROM unrelated").Scan(&value); err != nil {
				t.Fatal(err)
			}
			if journal != "delete" || value != "keep" {
				t.Fatalf("journal=%q value=%q", journal, value)
			}
		})
	}
}
