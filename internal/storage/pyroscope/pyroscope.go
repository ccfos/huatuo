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

// Package pyroscope implements a write-only pprof storage backend.
package pyroscope

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage/driver"
)

const (
	defaultAppNamePrefix    = "huatuo"
	defaultTimeoutSeconds   = 5
	maxErrorResponseBytes   = 4 * 1024
	pprofFormat             = "pprof"
	protobufContentType     = "application/octet-stream"
	authorizationHeaderName = "Authorization"
)

// Config contains Pyroscope backend settings.
type Config struct {
	Address        string
	AppNamePrefix  string
	Username       string
	Password       string
	BearerToken    string
	TimeoutSeconds int
}

// Storage pushes protobuf-encoded pprof profiles to Pyroscope's HTTP ingest API.
type Storage struct {
	ingestURL     *url.URL
	appNamePrefix string
	username      string
	password      string
	bearerToken   string
	httpClient    *http.Client
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	driver.RegisterBackend("pyroscope", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(&Config{
			Address:        cfg.PyroscopeAddress,
			AppNamePrefix:  cfg.PyroscopeAppNamePrefix,
			Username:       cfg.PyroscopeUsername,
			Password:       cfg.PyroscopePassword,
			BearerToken:    cfg.PyroscopeBearerToken,
			TimeoutSeconds: cfg.PyroscopeTimeoutSeconds,
		})
	})
}

// NewBackend creates a Pyroscope storage backend.
func NewBackend(cfg *Config) (*Storage, error) {
	if cfg == nil {
		return nil, fmt.Errorf("pyroscope: config is nil")
	}

	endpoint, err := parseAddress(cfg.Address)
	if err != nil {
		return nil, err
	}
	username, password, token, err := validateAuthentication(cfg)
	if err != nil {
		return nil, err
	}

	timeoutSeconds := cfg.TimeoutSeconds
	switch {
	case timeoutSeconds < 0:
		return nil, fmt.Errorf("pyroscope: timeout seconds must not be negative")
	case timeoutSeconds == 0:
		timeoutSeconds = defaultTimeoutSeconds
	}

	appNamePrefix := sanitizeNamePart(strings.TrimSpace(cfg.AppNamePrefix))
	if appNamePrefix == "" {
		appNamePrefix = defaultAppNamePrefix
	}

	return newBackendWithHTTPClient(
		endpoint,
		appNamePrefix,
		username,
		password,
		token,
		&http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	), nil
}

func parseAddress(address string) (*url.URL, error) {
	rawAddress := strings.TrimSpace(address)
	if rawAddress == "" {
		return nil, fmt.Errorf("pyroscope: address is empty")
	}
	endpoint, err := url.Parse(rawAddress)
	if err != nil {
		return nil, fmt.Errorf("pyroscope: parse address: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf(
			"pyroscope: address scheme must be http or https, got %q",
			endpoint.Scheme,
		)
	}
	if endpoint.Host == "" || endpoint.Opaque != "" {
		return nil, fmt.Errorf("pyroscope: address must include a host")
	}
	if endpoint.User != nil {
		return nil, fmt.Errorf("pyroscope: URL credentials are not allowed; use authentication fields")
	}
	if endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, fmt.Errorf("pyroscope: address must not include a query or fragment")
	}

	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/ingest"
	endpoint.RawPath = ""
	return endpoint, nil
}

func validateAuthentication(cfg *Config) (string, string, string, error) {
	hasUsername := cfg.Username != ""
	hasPassword := cfg.Password != ""
	token := cfg.BearerToken
	switch {
	case hasUsername != hasPassword:
		return "", "", "", fmt.Errorf(
			"pyroscope: username and password must be configured together",
		)
	case strings.Contains(cfg.Username, ":"):
		return "", "", "", fmt.Errorf(
			"pyroscope: basic authentication username must not contain a colon",
		)
	case token != "" && hasUsername:
		return "", "", "", fmt.Errorf(
			"pyroscope: basic authentication and bearer token are mutually exclusive",
		)
	case containsControl(cfg.Username) || containsControl(cfg.Password) ||
		containsControl(token):
		return "", "", "", fmt.Errorf(
			"pyroscope: authentication fields must not contain control characters",
		)
	case strings.TrimSpace(token) != token:
		return "", "", "", fmt.Errorf(
			"pyroscope: bearer token must not have surrounding whitespace",
		)
	default:
		return cfg.Username, cfg.Password, token, nil
	}
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func newBackendWithHTTPClient(
	endpoint *url.URL,
	appNamePrefix string,
	username string,
	password string,
	bearerToken string,
	httpClient *http.Client,
) *Storage {
	return &Storage{
		ingestURL:     endpoint,
		appNamePrefix: appNamePrefix,
		username:      username,
		password:      password,
		bearerToken:   bearerToken,
		httpClient:    httpClient,
	}
}

// Init is a no-op because Pyroscope owns its profile schema.
func (b *Storage) Init(context.Context, string, []driver.Index) error {
	return nil
}

// Save uploads one pprof protobuf payload.
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

	req, err := http.NewRequestWithContext(
		driver.WithContext(ctx),
		http.MethodPost,
		endpoint.String(),
		bytes.NewReader(rec.Data),
	)
	if err != nil {
		return fmt.Errorf("pyroscope: create ingest request: %w", err)
	}
	req.Header.Set("Content-Type", protobufContentType)
	switch {
	case b.bearerToken != "":
		req.Header.Set(authorizationHeaderName, "Bearer "+b.bearerToken)
	case b.username != "":
		req.SetBasicAuth(b.username, b.password)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pyroscope: ingest profile: %w", err)
	}

	body, responseErr := readAndCloseResponse(resp)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.Join(strings.Fields(string(body)), " ")
		if message == "" {
			message = resp.Status
		}
		statusErr := fmt.Errorf(
			"pyroscope: ingest returned status %d: %s",
			resp.StatusCode,
			message,
		)
		return errors.Join(statusErr, responseErr)
	}
	if responseErr != nil {
		return fmt.Errorf("pyroscope: consume ingest response: %w", responseErr)
	}
	return nil
}

func readAndCloseResponse(resp *http.Response) ([]byte, error) {
	if resp.Body == nil {
		return nil, fmt.Errorf("response body is nil")
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseBytes))
	_, drainErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	return body, errors.Join(readErr, drainErr, closeErr)
}

// Get is unsupported because Pyroscope owns profile queries.
func (b *Storage) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupportedOp
}

// Delete is unsupported because Pyroscope owns profile retention.
func (b *Storage) Delete(context.Context, string) error {
	return driver.ErrUnsupportedOp
}

// Query is unsupported because Pyroscope owns profile queries.
func (b *Storage) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, driver.ErrUnsupportedOp
}

// Count is unsupported because Pyroscope owns profile queries.
func (b *Storage) Count(context.Context, driver.Query) (int64, error) {
	return 0, driver.ErrUnsupportedOp
}

// Values is unsupported because Pyroscope owns profile queries.
func (b *Storage) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, driver.ErrUnsupportedOp
}

// Close releases no resources because the HTTP client owns no background worker.
func (b *Storage) Close(context.Context) error {
	return nil
}

func (b *Storage) applicationName(fields map[string]any) string {
	name := b.appNamePrefix
	if tracerName := sanitizeNamePart(stringField(fields, "tracer_name")); tracerName != "" {
		name += "." + tracerName
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
		{name: "profile_type", field: "profile_type"},
	}
	for _, name := range profiler.CollectionDimensionLabelNames() {
		if name == profiler.LabelContainerID {
			continue
		}
		labels = append(labels, struct {
			name  string
			field string
		}{name: name, field: name})
	}

	values := make([]string, 0, len(labels))
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
	return sanitizeProfilePart(value, true)
}

func sanitizeLabelValue(value string) string {
	return sanitizeProfilePart(value, false)
}

func sanitizeProfilePart(value string, allowDot bool) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, current := range strings.TrimSpace(value) {
		if unicode.IsLetter(current) || unicode.IsDigit(current) ||
			current == '_' || current == '-' || current == '/' ||
			(allowDot && current == '.') {
			result.WriteRune(current)
			continue
		}
		result.WriteByte('_')
	}
	return strings.Trim(result.String(), "_")
}
