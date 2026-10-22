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

package escompatible

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"

	es "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

type Config struct {
	BackendType   string
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
	client      *es.Client
	transport   *http.Transport
	index       string
	backendType string
	fieldsMu    sync.RWMutex
	fields      *fieldSchema
	fieldsAt    time.Time
	fieldsGate  chan struct{}
}

func New(cfg *Config) (*Reader, error) {
	if cfg == nil || strings.TrimSpace(cfg.Index) == "" {
		return nil, fmt.Errorf("MCP Elasticsearch index is required")
	}
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("MCP Elasticsearch address is required")
	}
	kind := cfg.BackendType
	if kind == "" {
		kind = constant.EventBackendElasticsearch
	}
	if kind != constant.EventBackendElasticsearch && kind != constant.EventBackendOpenSearch {
		return nil, fmt.Errorf("unsupported Elasticsearch adapter type %q", kind)
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, fmt.Errorf("MCP Elasticsearch username and password must be configured together")
	}
	for _, address := range cfg.Addresses {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("MCP Elasticsearch address must be an absolute HTTP URL without credentials, query, or fragment")
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: constant.ESDialTimeout, KeepAlive: constant.HTTPKeepAlive}).DialContext,
		ResponseHeaderTimeout: constant.ESResponseHeaderTimeout,
		TLSHandshakeTimeout:   constant.TLSHandshakeTimeout,
		ExpectContinueTimeout: constant.HTTPExpectContinueTimeout,
		MaxIdleConns:          constant.HTTPMaxIdleConnections,
		IdleConnTimeout:       constant.ESIdleConnectionTimeout,
	}
	// Preserve the existing ES deployment trust policy without changing the
	// framework's shared transport or connection pool.
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402
	client, err := es.NewClient(es.Config{
		Addresses: cfg.Addresses, Username: cfg.Username, Password: cfg.Password,
		DisableRetry: true, EnableCompatibilityMode: true,
		Transport: &readTransport{inner: transport, beforeRequest: cfg.BeforeRequest},
	})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("create MCP Elasticsearch client: %w", err)
	}
	// Do not probe optional diagnostic backends during API server startup.
	return &Reader{client: client, transport: transport, index: cfg.Index, backendType: kind, fieldsGate: make(chan struct{}, 1)}, nil
}

var (
	_ event.ManagedReader = (*Reader)(nil)
	_ event.TracerCounter = (*Reader)(nil)
)

func (r *Reader) BackendType() string { return r.backendType }

func (r *Reader) Close(_ context.Context) error {
	if r != nil {
		r.transport.CloseIdleConnections()
	}
	return nil
}

// readTransport admits only search/count/field-capability reads and charges each network
// attempt before dispatch. OpenSearch rejects ES vendor media types, while
// older ES versions omit the product header required by the v8 SDK.
type readTransport struct {
	inner         http.RoundTripper
	beforeRequest func(context.Context) error
}

func (t *readTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || (!strings.HasSuffix(req.URL.Path, constant.ESSearchPathSuffix) && !strings.HasSuffix(req.URL.Path, constant.ESCountPathSuffix) && !strings.HasSuffix(req.URL.Path, constant.ESFieldCapsPathSuffix)) {
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
	req.Header.Set(constant.HeaderContentType, constant.MIMEJSON)
	req.Header.Set(constant.HeaderAccept, constant.MIMEJSON)
	res, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if res.Header == nil {
		res.Header = make(http.Header)
	}
	if res.Header.Get(constant.HeaderElasticProduct) == "" {
		res.Header.Set(constant.HeaderElasticProduct, constant.BackendNameES)
	}
	return res, nil
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
	case constant.ESSearchOperation:
		response, err = (esapi.SearchRequest{Index: []string{index}, Body: bytes.NewReader(encoded)}).Do(ctx, r.client)
	case constant.ESCountOperation:
		response, err = (esapi.CountRequest{Index: []string{index}, Body: bytes.NewReader(encoded)}).Do(ctx, r.client)
	case constant.ESFieldCapsOperation:
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
		data, readErr := io.ReadAll(io.LimitReader(response.Body, constant.MaxBackendErrorBodyBytes+1))
		if readErr != nil {
			return &event.ReadError{Err: fmt.Errorf("read Elasticsearch error body: %w", readErr), Transient: true}
		}
		if len(data) > constant.MaxBackendErrorBodyBytes {
			data = append(data[:constant.MaxBackendErrorBodyBytes], "... (truncated)"...)
		}
		return &ResponseError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, constant.MaxESResponseBytes+1))
	if err != nil {
		return &event.ReadError{Err: fmt.Errorf("read Elasticsearch response: %w", err), Transient: true}
	}
	if len(data) > constant.MaxESResponseBytes {
		return fmt.Errorf("%w: response exceeds %d bytes", event.ErrDecodeFailed, constant.MaxESResponseBytes)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %w", event.ErrDecodeFailed, err)
	}
	return nil
}
