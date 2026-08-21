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
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ElasticsearchConfig keeps storage opt-in explicit so incomplete credentials
// cannot silently disable persistence.
type ElasticsearchConfig struct {
	Address  string
	Username string
	Password string
	Index    string `default:"huatuo_bamai"`
}

// Enabled reports whether all connection fields opt in to Elasticsearch.
func (c ElasticsearchConfig) Enabled() bool {
	return strings.TrimSpace(c.Address) != "" &&
		strings.TrimSpace(c.Username) != "" &&
		strings.TrimSpace(c.Password) != ""
}

// Validate accepts either a complete connection or no connection fields.
func (c ElasticsearchConfig) Validate() error {
	fields := []string{c.Address, c.Username, c.Password}
	configured := 0
	for _, field := range fields {
		if strings.TrimSpace(field) != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != len(fields) {
		return errors.New("address, username, and password must be configured together")
	}

	for _, address := range strings.Split(c.Address, ",") {
		trimmed := strings.TrimSpace(address)
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid address %q", address)
		}
	}
	return nil
}
