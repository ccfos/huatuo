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

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	es "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	storagees "github.com/ccfos/huatuo/internal/storage/elasticsearch"
)

const (
	AuthTypeNone  = "none"
	AuthTypeBasic = "basic"
)

const (
	MaxESResponseBytes    = 8 << 20
	ESFieldCapsPathSuffix = "/_field_caps"
	ESSearchPathSuffix    = "/_search"
	ESCountPathSuffix     = "/_count"
)

type Config struct {
	Addresses     []string
	Username      string
	Password      string
	Index         string
	BeforeRequest func(context.Context) error
}

// Reader owns no writer, bulk worker, or server-side cursor. Each operation
// is one bounded data request, with lazily cached field capabilities. The Tool
// layer owns retries and total timeout; every HTTP read charges its budget.
type Reader struct {
	client     *storagees.ReadClient
	index      string
	fieldsMu   sync.RWMutex
	fields     *fieldSchema
	fieldsAt   time.Time
	fieldsGate chan struct{}
}

func New(cfg *Config) (*Reader, error) {
	if cfg == nil || strings.TrimSpace(cfg.Index) == "" {
		return nil, fmt.Errorf("MCP Elasticsearch index is required")
	}
	beforeRequest := cfg.BeforeRequest
	client, err := storagees.NewReadClient(&storagees.ReadClientConfig{
		Config: es.Config{
			Addresses: cfg.Addresses, Username: cfg.Username, Password: cfg.Password,
		},
		WrapTransport: func(inner http.RoundTripper) http.RoundTripper {
			return &readTransport{inner: inner, beforeRequest: beforeRequest}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create MCP Elasticsearch client: %w", err)
	}
	// Do not probe optional diagnostic backends during API server startup.
	return &Reader{client: client, index: cfg.Index, fieldsGate: make(chan struct{}, 1)}, nil
}

var (
	_ event.ManagedReader = (*Reader)(nil)
	_ event.TracerCounter = (*Reader)(nil)
)

// Elasticsearch identifies the shared wire protocol, including OpenSearch reads.
func (r *Reader) BackendType() string { return event.EventBackendElasticsearch }

func (r *Reader) Close(_ context.Context) error {
	if r != nil {
		return r.client.Close()
	}
	return nil
}

// readTransport admits only search/count/field-capability reads and charges each
// network attempt before dispatch. Wire compatibility belongs to framework storage.
type readTransport struct {
	inner         http.RoundTripper
	beforeRequest func(context.Context) error
}

func (t *readTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || (!strings.HasSuffix(req.URL.Path, ESSearchPathSuffix) && !strings.HasSuffix(req.URL.Path, ESCountPathSuffix) && !strings.HasSuffix(req.URL.Path, ESFieldCapsPathSuffix)) {
		return nil, fmt.Errorf("%w: only Elasticsearch search, count and field capabilities are allowed", event.ErrInvalidQuery)
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if t.beforeRequest != nil {
		if err := t.beforeRequest(req.Context()); err != nil {
			return nil, err
		}
	}
	return t.inner.RoundTrip(req)
}

func (r *Reader) request(ctx context.Context, operation string, body, target any) error {
	return r.requestIndex(ctx, operation, r.index, body, target)
}

func (r *Reader) requestIndex(ctx context.Context, operation, index string, body, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encode query: %w", event.ErrInvalidQuery, err)
	}
	var response *esapi.Response
	switch operation {
	case ESSearchOperation:
		response, err = (esapi.SearchRequest{Index: []string{index}, Body: bytes.NewReader(encoded)}).Do(ctx, r.client)
	case ESCountOperation:
		response, err = (esapi.CountRequest{Index: []string{index}, Body: bytes.NewReader(encoded)}).Do(ctx, r.client)
	case ESFieldCapsOperation:
		selector, ok := body.(string)
		if !ok {
			return fmt.Errorf("%w: field capabilities require an index selector", event.ErrInvalidQuery)
		}
		includeUnmapped, ignoreUnavailable := true, true
		response, err = (esapi.FieldCapsRequest{Index: []string{selector}, Fields: schemaFieldNames(), IncludeUnmapped: &includeUnmapped, IgnoreUnavailable: &ignoreUnavailable}).Do(ctx, r.client)
	default:
		return fmt.Errorf("%w: unknown read operation", event.ErrInvalidQuery)
	}
	if err != nil {
		var networkError net.Error
		transient := errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		return &event.ReadError{Err: fmt.Errorf("MCP Elasticsearch %s: %w", operation, err), Transient: transient}
	}
	defer response.Body.Close()
	if response.IsError() {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, event.MaxBackendErrorBodyBytes+1))
		if readErr != nil {
			return &event.ReadError{Err: fmt.Errorf("read Elasticsearch error body: %w", readErr), Transient: true}
		}
		if len(data) > event.MaxBackendErrorBodyBytes {
			data = append(data[:event.MaxBackendErrorBodyBytes], "... (truncated)"...)
		}
		return &ResponseError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxESResponseBytes+1))
	if err != nil {
		return &event.ReadError{Err: fmt.Errorf("read Elasticsearch response: %w", err), Transient: true}
	}
	if len(data) > MaxESResponseBytes {
		return fmt.Errorf("%w: response exceeds %d bytes", event.ErrDecodeFailed, MaxESResponseBytes)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %w", event.ErrDecodeFailed, err)
	}
	return nil
}
