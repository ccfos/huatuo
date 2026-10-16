// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// openDB opens a SQLite connection and configures the connection pool.
//
// The DSN enables WAL journaling and synchronous=NORMAL. The default
// rollback-journal mode forces an fsync per write transaction and blocks
// readers while a writer holds the lock; WAL removes both, and
// synchronous=NORMAL keeps durability within one power loss of the WAL
// checkpoint instead of per-commit._busy_timeout tolerates the remaining
// writer contention on the single pooled connection.
func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", withPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("sqlite backend: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)
	db.SetConnMaxIdleTime(30 * time.Minute)
	return db, nil
}

// withPragmas appends the backend's connection pragmas to the DSN, keeping
// any query parameters the caller already supplied.
func withPragmas(dsn string) string {
	extra := "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	if strings.Contains(dsn, "?") {
		return dsn + "&" + extra
	}
	return dsn + "?" + extra
}
