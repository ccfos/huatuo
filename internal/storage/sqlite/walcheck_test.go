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
	if sync != 1 { // NORMAL
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", sync)
	}
}
