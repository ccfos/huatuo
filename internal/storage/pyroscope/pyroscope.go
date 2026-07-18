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

// Package pyroscope implements a write-only storage backend for pprof profiles.
package pyroscope

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/storage/driver"
)

const (
	defaultAppNamePrefix  = "huatuo"
	defaultTimeoutSeconds = 5
	pprofFormat           = "pprof"
)

// Config contains Pyroscope backend settings.
type Config struct {
	Address        string
	AppNamePrefix  string
	TimeoutSeconds int
}

// Storage pushes protobuf-encoded pprof profiles to Pyroscope's HTTP ingest API.
// Query operations are intentionally unsupported; Grafana queries Pyroscope
// directly.
type Storage struct {
	ingestURL     *url.URL
	appNamePrefix string
	httpClient    *http.Client
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	driver.RegisterBackend("pyroscope", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(&Config{
			Address:        cfg.PyroscopeAddress,
			AppNamePrefix:  cfg.PyroscopeAppNamePrefix,
			TimeoutSeconds: cfg.PyroscopeTimeoutSeconds,
		})
	})
}

// NewBackend creates a Pyroscope storage backend.
func NewBackend(cfg *Config) (*Storage, error) {
	if cfg == nil {
		return nil, fmt.Errorf("pyroscope: config is nil")
	}

	endpoint, err := url.Parse(strings.TrimSpace(cfg.Address))
	if err != nil {
		return nil, fmt.Errorf("pyroscope: parse address: %w", err)
	}
	if endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("pyroscope: invalid address %q", cfg.Address)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/ingest"

	appNamePrefix := strings.TrimSpace(cfg.AppNamePrefix)
	if appNamePrefix == "" {
		appNamePrefix = defaultAppNamePrefix
	}
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTimeoutSeconds
	}

	return newBackendWithHTTPClient(
		endpoint,
		appNamePrefix,
		&http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	), nil
}

func newBackendWithHTTPClient(endpoint *url.URL, appNamePrefix string, httpClient *http.Client) *Storage {
	return &Storage{
		ingestURL:     endpoint,
		appNamePrefix: appNamePrefix,
		httpClient:    httpClient,
	}
}

func (b *Storage) Init(context.Context, string, []driver.Index) error {
	return nil
}

func (b *Storage) Save(ctx context.Context, rec driver.Record) error {
	if len(rec.Data) == 0 {
		return fmt.Errorf("pyroscope: profile data is empty")
	}

	endpoint := *b.ingestURL
	start := profileStart(rec.Fields)
	end := profileEnd(rec.Fields, start)
	query := endpoint.Query()
	query.Set("name", b.applicationName(rec.Fields))
	query.Set("from", strconv.FormatInt(start.Unix(), 10))
	query.Set("until", strconv.FormatInt(end.Unix(), 10))
	query.Set("format", pprofFormat)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(rec.Data))
	if err != nil {
		return fmt.Errorf("pyroscope: create ingest request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pyroscope: ingest profile: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("pyroscope: ingest returned status %d: %s", resp.StatusCode, message)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (b *Storage) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupportedOp
}

func (b *Storage) Delete(context.Context, string) error {
	return driver.ErrUnsupportedOp
}

func (b *Storage) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, driver.ErrUnsupportedOp
}

func (b *Storage) Count(context.Context, driver.Query) (int64, error) {
	return 0, driver.ErrUnsupportedOp
}

func (b *Storage) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, driver.ErrUnsupportedOp
}

func (b *Storage) Close(context.Context) error {
	return nil
}

func (b *Storage) applicationName(fields map[string]any) string {
	name := b.appNamePrefix
	if tracerName := stringField(fields, "tracer_name"); tracerName != "" {
		name += "." + sanitizeNamePart(tracerName)
	}

	labels := []struct {
		name  string
		field string
	}{
		{name: "tracer_id", field: "tracer_id"},
		{name: "tracer_name", field: "tracer_name"},
		{name: "hostname", field: "hostname"},
		{name: "region", field: "region"},
		{name: "container_id", field: "container_id"},
		{name: "container_hostname", field: "container_hostname"},
		{name: "tracer_type", field: "tracer_type"},
	}

	var values []string
	for _, label := range labels {
		value := sanitizeLabelValue(stringField(fields, label.field))
		if value == "" {
			continue
		}
		values = append(values, label.name+"="+value)
	}
	if len(values) > 0 {
		name += "{" + strings.Join(values, ",") + "}"
	}
	return name
}

func profileStart(fields map[string]any) time.Time {
	if value, ok := timeField(fields, "profile_start_time"); ok {
		return value.UTC()
	}
	if value, ok := timeField(fields, "tracer_time"); ok {
		return value.UTC()
	}
	if value, ok := timeField(fields, "uploaded_time"); ok {
		return value.UTC()
	}
	return time.Now().UTC()
}

func profileEnd(fields map[string]any, start time.Time) time.Time {
	if value, ok := timeField(fields, "profile_end_time"); ok && value.After(start) {
		return value.UTC()
	}
	return start.Add(time.Second)
}

func timeField(fields map[string]any, name string) (time.Time, bool) {
	value, ok := fields[name]
	if !ok {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case time.Time:
		return typed, !typed.IsZero()
	case *time.Time:
		if typed == nil {
			return time.Time{}, false
		}
		return *typed, !typed.IsZero()
	default:
		return time.Time{}, false
	}
}

func stringField(fields map[string]any, name string) string {
	value, _ := fields[name].(string)
	return strings.TrimSpace(value)
}

func sanitizeNamePart(value string) string {
	replacer := strings.NewReplacer(
		".", "_", ",", "_", "{", "_", "}", "_", "\"", "_", "\\", "_", "\r", " ", "\n", " ",
	)
	return strings.TrimSpace(replacer.Replace(value))
}

func sanitizeLabelValue(value string) string {
	replacer := strings.NewReplacer(",", "_", "{", "_", "}", "_", "\"", "_", "\\", "_", "\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(value))
}
