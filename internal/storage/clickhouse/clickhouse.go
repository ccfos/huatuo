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

// Package clickhouse implements a write-only ClickHouse storage backend.
package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"huatuo-bamai/internal/storage/driver"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const requestTimeout = 30 * time.Second

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Storage writes HUATUO documents through ClickHouse's HTTP interface.
type Storage struct {
	client     httpDoer
	address    string
	username   string
	password   string
	database   string
	table      string
	collection string
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	driver.RegisterBackend("clickhouse", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(cfg.ClickHouseAddress, cfg.ClickHouseUsername, cfg.ClickHousePassword, cfg.ClickHouseDatabase, cfg.ClickHouseTable)
	})
}

// NewBackend creates a ClickHouse event store using the HTTP protocol.
func NewBackend(address, username, password, database, table string) (*Storage, error) {
	u, err := url.Parse(strings.TrimRight(address, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("ClickHouse backend: invalid HTTP address %q", address)
	}
	if !identifierPattern.MatchString(database) || !identifierPattern.MatchString(table) {
		return nil, errors.New("ClickHouse backend: database and table must be identifiers")
	}
	return &Storage{
		client:   &http.Client{Timeout: requestTimeout},
		address:  strings.TrimRight(address, "/"),
		username: username,
		password: password,
		database: database,
		table:    table,
	}, nil
}

func (s *Storage) Init(ctx context.Context, collection string, _ []driver.Index) error {
	if strings.TrimSpace(collection) == "" {
		return errors.New("ClickHouse backend: collection must not be empty")
	}
	s.collection = collection
	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`.`%s` (collection LowCardinality(String), id String, data String, fields String, observed_at DateTime64(3, 'UTC') DEFAULT now64(3)) ENGINE = MergeTree ORDER BY (collection, observed_at, id)", s.database, s.table)
	return s.execute(ctx, query, nil)
}

func (s *Storage) Save(ctx context.Context, rec driver.Record) error {
	if rec.ID == "" {
		return fmt.Errorf("%w: empty id", driver.ErrInvalidField)
	}
	if s.collection == "" {
		return errors.New("ClickHouse backend: not initialized")
	}
	fields, err := json.Marshal(rec.Fields)
	if err != nil {
		return fmt.Errorf("ClickHouse backend: encode fields: %w", err)
	}
	row, err := json.Marshal(struct {
		Collection string `json:"collection"`
		ID         string `json:"id"`
		Data       string `json:"data"`
		Fields     string `json:"fields"`
	}{s.collection, rec.ID, string(rec.Data), string(fields)})
	if err != nil {
		return fmt.Errorf("ClickHouse backend: encode row: %w", err)
	}
	row = append(row, '\n')
	query := fmt.Sprintf("INSERT INTO `%s`.`%s` (collection, id, data, fields) FORMAT JSONEachRow", s.database, s.table)
	return s.execute(ctx, query, row)
}

func (s *Storage) execute(ctx context.Context, query string, body []byte) error {
	u, err := url.Parse(s.address)
	if err != nil {
		return err
	}
	values := u.Query()
	values.Set("query", query)
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(driver.WithContext(ctx), http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ClickHouse backend: create request: %w", err)
	}
	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("ClickHouse backend: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ClickHouse backend: status %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (s *Storage) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupported
}
func (s *Storage) Delete(context.Context, string) error { return driver.ErrUnsupported }
func (s *Storage) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, driver.ErrUnsupported
}

func (s *Storage) Count(context.Context, driver.Query) (int64, error) {
	return 0, driver.ErrUnsupported
}

func (s *Storage) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, driver.ErrUnsupported
}
func (s *Storage) Close(context.Context) error { return nil }
