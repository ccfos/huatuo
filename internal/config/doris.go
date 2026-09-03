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

package config

import (
	"fmt"
	"strings"

	"huatuo-bamai/internal/storage/driver"
)

// Group commit modes accepted by GroupCommit.
const (
	groupCommitOff   = "off_mode"
	groupCommitSync  = "sync_mode"
	groupCommitAsync = "async_mode"
)

// DorisConfig keeps storage opt-in explicit so a half-filled section fails
// loudly instead of silently disabling storage.
type DorisConfig struct {
	// MySQLAddr is the FE query port (host:port), used for DDL and queries.
	MySQLAddr string
	// HTTPAddr is the FE HTTP port (host:port), used for Stream Load writes.
	HTTPAddr string
	Database string `default:"huatuo_bamai"`
	Username string
	Password string

	// PartitionField is the time field tables are range-partitioned on;
	// empty creates a single ever-growing partition.
	PartitionField string `default:"uploaded_time"`
	RetentionDays  int    `default:"30"`

	Buckets  int `default:"4"`
	Replicas int `default:"1"`

	// GroupCommit merges loads server-side: off_mode, sync_mode or
	// async_mode. Enable it when many agents write one table.
	GroupCommit string `default:"off_mode"`

	// MaxRetries bounds retries of a transient failure; the batch is dropped
	// once they are exhausted. 0 disables retrying.
	MaxRetries           int `default:"3"`
	BatchMaxRows         int `default:"16"`
	FlushIntervalSeconds int `default:"5"`
}

// Enabled reports whether both endpoints opt in to Doris.
func (c DorisConfig) Enabled() bool {
	return strings.TrimSpace(c.MySQLAddr) != "" && strings.TrimSpace(c.HTTPAddr) != ""
}

// Validate accepts either both endpoints or neither.
func (c DorisConfig) Validate() error {
	mysqlSet := strings.TrimSpace(c.MySQLAddr) != ""
	httpSet := strings.TrimSpace(c.HTTPAddr) != ""

	if !mysqlSet && !httpSet {
		return nil
	}
	if !mysqlSet || !httpSet {
		return fmt.Errorf("Doris MySQLAddr and HTTPAddr must be set together")
	}
	if strings.TrimSpace(c.Database) == "" {
		return fmt.Errorf("Doris Database must not be empty")
	}
	switch strings.TrimSpace(c.GroupCommit) {
	case "", groupCommitOff, groupCommitSync, groupCommitAsync:
	default:
		return fmt.Errorf("Doris GroupCommit %q must be %s, %s or %s",
			c.GroupCommit, groupCommitOff, groupCommitSync, groupCommitAsync)
	}

	return nil
}

// DriverConfig renders the storage driver configuration for this section.
func (c DorisConfig) DriverConfig() *driver.Config {
	return &driver.Config{
		Driver:                    "doris",
		DorisMySQLAddr:            strings.TrimSpace(c.MySQLAddr),
		DorisHTTPAddr:             strings.TrimSpace(c.HTTPAddr),
		DorisDatabase:             strings.TrimSpace(c.Database),
		DorisUsername:             c.Username,
		DorisPassword:             c.Password,
		DorisBuckets:              c.Buckets,
		DorisReplicas:             c.Replicas,
		DorisBatchMaxRows:         c.BatchMaxRows,
		DorisFlushIntervalSeconds: c.FlushIntervalSeconds,
		DorisMaxRetries:           c.MaxRetries,
		DorisPartitionField:       strings.TrimSpace(c.PartitionField),
		DorisRetentionDays:        c.RetentionDays,
		DorisGroupCommit:          strings.TrimSpace(c.GroupCommit),
	}
}
