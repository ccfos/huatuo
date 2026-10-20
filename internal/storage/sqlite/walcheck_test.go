package sqlite

import "testing"

func TestOpenDBEnablesWAL(t *testing.T) {
	db, err := openDB(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var sync int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	// synchronous stays at SQLite's durable FULL default (2); operators
	// may opt into NORMAL via the DSN.
	if sync != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL default)", sync)
	}
}
