package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func benchWrites(b *testing.B, dsn string) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO t (v) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWritesRollbackJournal(b *testing.B) { benchWrites(b, b.TempDir()+"/a.db") }
func BenchmarkWritesWAL(b *testing.B) {
	benchWrites(b, b.TempDir()+"/b.db?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
}
