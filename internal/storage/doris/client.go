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

package doris

import (
	"database/sql"
	"fmt"
	"net"
	"time"

	"huatuo-bamai/internal/storage/driver"

	"github.com/go-sql-driver/mysql"
)

// openDB opens the query connection. Doris speaks the MySQL protocol on the FE
// query port; the database is not selected in the DSN because Init creates it.
func openDB(cfg *driver.Config) (*sql.DB, error) {
	if _, _, err := net.SplitHostPort(cfg.DorisMySQLAddr); err != nil {
		return nil, fmt.Errorf("doris backend: MySQLAddr %q: %w", cfg.DorisMySQLAddr, err)
	}

	dsnConfig := mysql.NewConfig()
	dsnConfig.Net = "tcp"
	dsnConfig.Addr = cfg.DorisMySQLAddr
	dsnConfig.User = cfg.DorisUsername
	dsnConfig.Passwd = cfg.DorisPassword
	dsnConfig.ParseTime = false
	// Profile payloads are multi-megabyte; server-side prepared statements add
	// a round trip per query without helping here.
	dsnConfig.InterpolateParams = true

	db, err := sql.Open("mysql", dsnConfig.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("doris backend: open database: %w", err)
	}

	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)
	db.SetConnMaxIdleTime(30 * time.Minute)

	return db, nil
}
