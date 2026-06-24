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

// Package elasticsearch implements a storage backend compatible with
// Elasticsearch v7/v8 and OpenSearch using the official go-elasticsearch/v8 SDK.
package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/elastic/go-elasticsearch/v8/esutil"
	escount "github.com/elastic/go-elasticsearch/v8/typedapi/core/count"
	esget "github.com/elastic/go-elasticsearch/v8/typedapi/core/get"
	essearch "github.com/elastic/go-elasticsearch/v8/typedapi/core/search"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage/driver"
)

const (
	defaultIndex     = "huatuo_bamai"
	defaultQuerySize = 10000

	// Bulk indexer tuning. 5MB / 1s matches the upstream defaults and is a
	// safe starting point for ES/OpenSearch single-node and small clusters.
	// Adjust if write rate or per-event size drifts significantly.
	bulkFlushBytes    = 5 * 1024 * 1024
	bulkFlushInterval = time.Second
	bulkNumWorkers    = 4
)

// Config contains Elasticsearch backend settings.
type Config struct {
	Addresses          []string
	Username           string
	Password           string
	Index              string
	CAFile             string
	CertFile           string
	KeyFile            string
	InsecureSkipVerify *bool
}

// Storage stores records in Elasticsearch, OpenSearch, or any compatible backend.
//
// Save is asynchronous: items are queued in bulk and flushed by size, time, or
// Close. A nil error from Save means the item was buffered, not that it landed
// in the index. Permanent per-item failures are logged via OnFailure; transient
// whole-batch failures (429, 5xx, transport errors) are retried by the client.
// Call Close on shutdown to flush any pending events.
type Storage struct {
	transport esapi.Transport
	bulk      esutil.BulkIndexer
	index     string
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	factory := func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(&Config{
			Addresses:          cfg.ESAddresses,
			Username:           cfg.ESUsername,
			Password:           cfg.ESPassword,
			Index:              cfg.ESIndex,
			CAFile:             cfg.ESCAFile,
			CertFile:           cfg.ESCertFile,
			KeyFile:            cfg.ESKeyFile,
			InsecureSkipVerify: cfg.ESInsecureSkipVerify,
		})
	}
	driver.RegisterBackend("elasticsearch", factory)
	driver.RegisterBackend("opensearch", factory)
}

// NewBackend creates a backend that connects to Elasticsearch v7/v8 or OpenSearch.
func NewBackend(cfg *Config) (*Storage, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	prefix := cfg.Index
	if prefix == "" {
		prefix = defaultIndex
	}
	client, err := newCompatClient(cfg)
	if err != nil {
		return nil, err
	}

	bulk, err := esutil.NewBulkIndexer(esutil.BulkIndexerConfig{
		Client:        client,
		Index:         prefix,
		NumWorkers:    bulkNumWorkers,
		FlushBytes:    bulkFlushBytes,
		FlushInterval: bulkFlushInterval,
		OnError: func(_ context.Context, err error) {
			log.Errorf("elasticsearch bulk: %v", err)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("elasticsearch bulk indexer: %w", err)
	}

	return &Storage{transport: client, bulk: bulk, index: prefix}, nil
}

// Close flushes any pending bulk operations and stops the indexer workers.
// After Close, Save will panic; the Storage must not be reused.
func (s *Storage) Close(ctx context.Context) error {
	if s.bulk == nil {
		return nil
	}
	return s.bulk.Close(ctx)
}

func (s *Storage) Init(_ context.Context, _ string, indexes []driver.Index) error {
	for _, idx := range indexes {
		if err := validateFieldName(idx.Field); err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) Save(ctx context.Context, rec driver.Record) error {
	item := esutil.BulkIndexerItem{
		Index:      s.index,
		Action:     "index",
		DocumentID: rec.ID,
		Body:       bytes.NewReader(rec.Data),
		OnFailure: func(_ context.Context, _ esutil.BulkIndexerItem, res esutil.BulkIndexerResponseItem, err error) {
			// Reached only after client-level retries are exhausted, or the
			// failure is per-item (parsing, mapping, version conflict). The
			// item is dropped — caller does not learn about this synchronously.
			if err != nil {
				log.Errorf("elasticsearch bulk save %s/%s: %v", s.index, rec.ID, err)
				return
			}
			log.Errorf("elasticsearch bulk save %s/%s: status=%d type=%s reason=%s",
				s.index, rec.ID, res.Status, res.Error.Type, res.Error.Reason)
		},
	}
	if err := s.bulk.Add(driver.WithContext(ctx), item); err != nil {
		return fmt.Errorf("elasticsearch backend save %s: %w", s.index, err)
	}
	log.Debugf("elasticsearch bulk queued index=%s id=%s data=%s", s.index, rec.ID, rec.Data)
	return nil
}

func (s *Storage) Get(ctx context.Context, id string) (rec driver.Record, err error) {
	req := esapi.GetRequest{Index: s.index, DocumentID: id}
	res, err := req.Do(driver.WithContext(ctx), s.transport)
	if err != nil {
		return rec, fmt.Errorf("elasticsearch backend get %s/%s: %w", s.index, id, err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return rec, driver.ErrNotFound
	}
	if res.IsError() {
		return rec, responseError("get document", s.index, res)
	}

	var payload esget.Response
	if err = json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return rec, fmt.Errorf("elasticsearch backend get %s/%s: decode: %w", s.index, id, err)
	}
	if !payload.Found {
		return rec, driver.ErrNotFound
	}
	recordID := payload.Id_
	if recordID == "" {
		recordID = id
	}
	return driver.Record{ID: recordID, Data: driver.CloneBytes(payload.Source_)}, nil
}

func (s *Storage) Delete(ctx context.Context, id string) error {
	req := esapi.DeleteRequest{Index: s.index, DocumentID: id, Refresh: "true"}
	res, err := req.Do(driver.WithContext(ctx), s.transport)
	if err != nil {
		return fmt.Errorf("elasticsearch backend delete %s/%s: %w", s.index, id, err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.IsError() {
		return responseError("delete document", s.index, res)
	}
	return nil
}

func (s *Storage) Query(ctx context.Context, q driver.Query) ([]driver.Record, error) {
	body, err := buildSearchRequest(q)
	if err != nil {
		return nil, err
	}

	req := esapi.SearchRequest{Index: []string{s.index}, Body: bytes.NewReader(body)}
	res, err := req.Do(driver.WithContext(ctx), s.transport)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch backend query %s: %w", s.index, err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, responseError("query documents", s.index, res)
	}

	var payload essearch.Response
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("elasticsearch backend query %s: decode: %w", s.index, err)
	}
	records := make([]driver.Record, 0, len(payload.Hits.Hits))
	for i := range payload.Hits.Hits {
		hit := &payload.Hits.Hits[i]
		id := ""
		if hit.Id_ != nil {
			id = *hit.Id_
		}
		records = append(records, driver.Record{ID: id, Data: driver.CloneBytes(hit.Source_)})
	}
	return records, nil
}

func (s *Storage) Count(ctx context.Context, q driver.Query) (int64, error) {
	body, err := buildCountRequest(q)
	if err != nil {
		return 0, err
	}

	req := esapi.CountRequest{Index: []string{s.index}, Body: bytes.NewReader(body)}
	res, err := req.Do(driver.WithContext(ctx), s.transport)
	if err != nil {
		return 0, fmt.Errorf("elasticsearch backend count %s: %w", s.index, err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return 0, responseError("count documents", s.index, res)
	}

	var payload escount.Response
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("elasticsearch backend count %s: decode: %w", s.index, err)
	}
	return payload.Count, nil
}

func (s *Storage) Values(ctx context.Context, field string, q driver.Query, size int) ([]string, error) {
	body, err := buildValuesRequest(field, q, size)
	if err != nil {
		return nil, err
	}

	req := esapi.SearchRequest{Index: []string{s.index}, Body: bytes.NewReader(body)}
	res, err := req.Do(driver.WithContext(ctx), s.transport)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch backend terms %s/%s: %w", s.index, field, err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, responseError("terms aggregation", s.index, res)
	}

	var payload valuesResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("elasticsearch backend terms %s/%s: decode: %w", s.index, field, err)
	}
	result := make([]string, 0, len(payload.Aggregations.Terms.Buckets))
	for _, bucket := range payload.Aggregations.Terms.Buckets {
		result = append(result, driver.StringValue(bucket.Key))
	}
	return result, nil
}

func responseError(action, target string, res *esapi.Response) error {
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("elasticsearch %s %s: status %d: read body: %w", action, target, res.StatusCode, err)
	}
	return fmt.Errorf("elasticsearch %s %s: status %d: %s", action, target, res.StatusCode, strings.TrimSpace(string(body)))
}
