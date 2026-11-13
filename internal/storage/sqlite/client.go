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
func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite backend: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if isMemoryDSN(dsn) {
		// SQLite memory databases belong to one connection. Evicting the only
		// connection creates a new, empty database.
		db.SetConnMaxLifetime(0)
		db.SetConnMaxIdleTime(0)
	} else {
		db.SetConnMaxLifetime(time.Hour)
		db.SetConnMaxIdleTime(30 * time.Minute)
	}
	return db, nil
}

func isMemoryDSN(dsn string) bool {
	if dsn == ":memory:" || strings.HasPrefix(dsn, "file::memory:") {
		return true
	}

	queryStart := strings.IndexByte(dsn, '?')
	if queryStart < 0 {
		return false
	}
	for _, parameter := range strings.Split(dsn[queryStart+1:], "&") {
		key, value, ok := strings.Cut(parameter, "=")
		if ok && key == "mode" && value == "memory" {
			return true
		}
	}
	return false
}
