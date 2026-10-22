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

// Package prometheus provides MCP's bounded, read-only metrics reader.
package prometheus

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

var (
	ErrInvalidRequest   = errors.New("invalid Prometheus read request")
	ErrTransport        = errors.New("Prometheus transport failed")
	ErrDecodeFailed     = errors.New("Prometheus response decoding failed")
	ErrResponseTooLarge = errors.New("Prometheus response exceeds 4 MiB")
)

type Config struct {
	Address       string
	AuthType      string
	AuthToken     string
	AuthUser      string
	AuthPass      string
	Timeout       time.Duration
	BeforeRequest func(context.Context) error
}

type ResponseError struct {
	StatusCode int
	ErrorType  string
	Detail     string
}

func (e *ResponseError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("Prometheus returned HTTP %d (%s): %s", e.StatusCode, e.ErrorType, e.Detail)
	}
	return fmt.Sprintf("Prometheus returned HTTP %d", e.StatusCode)
}
func (e *ResponseError) HTTPStatusCode() int { return e.StatusCode }

// RequestFunc permits caller-owned retry/observability decorators around each
// HTTP attempt, including individual discovery batches. The backend imports
// no Tool policy, logger, metrics, or retry implementation.
type RequestFunc func(context.Context, string, string, url.Values) ([]byte, error)

type Reader struct {
	baseURL                                 *url.URL
	client                                  *http.Client
	transport                               *http.Transport
	authType, authToken, authUser, authPass string
	beforeRequest                           func(context.Context) error
}

func New(cfg *Config) (*Reader, error) {
	if cfg == nil {
		return nil, errors.New("Prometheus reader configuration is required")
	}
	baseURL, err := url.Parse(cfg.Address)
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return nil, errors.New("Prometheus address must be an absolute HTTP URL")
	}
	authType, authToken, err := validateAuth(cfg.AuthType, cfg.AuthToken, cfg.AuthUser, cfg.AuthPass)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = constant.DefaultPromTimeout
	}
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: constant.PrometheusDialTimeout, KeepAlive: constant.HTTPKeepAlive}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: constant.HTTPMaxIdleConnections,
		IdleConnTimeout: constant.PrometheusIdleConnectionTimeout, TLSHandshakeTimeout: constant.TLSHandshakeTimeout,
		ExpectContinueTimeout: constant.HTTPExpectContinueTimeout,
	}
	return &Reader{
		baseURL: baseURL, transport: transport,
		client:   &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		authType: authType, authToken: authToken, authUser: cfg.AuthUser, authPass: cfg.AuthPass,
		beforeRequest: cfg.BeforeRequest,
	}, nil
}

func (r *Reader) Close(_ context.Context) error {
	if r != nil {
		r.transport.CloseIdleConnections()
	}
	return nil
}

// Request performs one attempt. Caller-owned context and the configured HTTP
// timeout bound it; retries remain a Tool responsibility.
func (r *Reader) Request(ctx context.Context, method, path string, values url.Values) ([]byte, error) {
	if err := validateReadRequest(method, path); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.beforeRequest != nil {
		if err := r.beforeRequest(ctx); err != nil {
			return nil, err
		}
	}
	endpoint := *r.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	var body io.Reader = http.NoBody
	if method == http.MethodPost {
		body = strings.NewReader(values.Encode())
	} else {
		endpoint.RawQuery = values.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("%w: create request: %w", ErrInvalidRequest, err)
	}
	request.Header.Set(constant.HeaderAccept, constant.MIMEJSON)
	if method == http.MethodPost {
		request.Header.Set(constant.HeaderContentType, constant.MIMEForm)
	}
	switch r.authType {
	case constant.AuthTypeBearer:
		request.Header.Set(constant.HeaderAuthorization, constant.BearerPrefix+r.authToken)
	case constant.AuthTypeBasic:
		request.SetBasicAuth(r.authUser, r.authPass)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: send request: %w", ErrTransport, err)
	}
	defer response.Body.Close()
	var input io.Reader = response.Body
	if strings.EqualFold(response.Header.Get(constant.HeaderContentEncoding), constant.EncodingGzip) {
		zipper, err := gzip.NewReader(response.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip response: %w", ErrDecodeFailed, err)
		}
		defer zipper.Close()
		input = zipper
	}
	data, err := io.ReadAll(io.LimitReader(input, constant.MaxPrometheusResponse+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %w", ErrTransport, err)
	}
	if response.StatusCode != http.StatusOK {
		var detail struct {
			ErrorType string `json:"errorType"`
			Error     string `json:"error"`
		}
		// Preserve proxy/plain-text errors as well as standard Prometheus errors.
		if len(data) <= constant.MaxPrometheusResponse {
			_ = json.Unmarshal(data, &detail)
		}
		if detail.ErrorType == "" {
			detail.ErrorType = constant.BackendErrorUnknown
		}
		if detail.Error == "" {
			detail.Error = strings.TrimSpace(string(data))
		}
		return nil, &ResponseError{
			StatusCode: response.StatusCode,
			ErrorType:  boundedErrorValue(detail.ErrorType, constant.MaxBackendErrorTypeBytes),
			Detail:     boundedErrorValue(detail.Error, constant.MaxBackendErrorBodyBytes),
		}
	}
	if len(data) > constant.MaxPrometheusResponse {
		return nil, ErrResponseTooLarge
	}
	if err := validateAPIResponse(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validateReadRequest(method, path string) error {
	allowed := false
	switch path {
	case constant.PrometheusQueryPath, constant.PrometheusRangeQueryPath:
		allowed = method == http.MethodGet || method == http.MethodPost
	case constant.PrometheusMetricNamesPath, constant.PrometheusSeriesPath, constant.PrometheusLabelsPath, constant.PrometheusMetadataPath:
		allowed = method == http.MethodGet
	default:
		return fmt.Errorf("%w: Prometheus endpoint %q is not allowed", ErrInvalidRequest, path)
	}
	if !allowed {
		return fmt.Errorf("%w: Prometheus method %q is not allowed for %q", ErrInvalidRequest, method, path)
	}
	return nil
}

func validateAuth(authType, authToken, authUser, authPass string) (string, string, error) {
	authType = strings.ToLower(strings.TrimSpace(authType))
	switch authType {
	case "", constant.AuthTypeNone:
		if authToken != "" || authUser != "" || authPass != "" {
			return "", "", errors.New("Prometheus credentials require bearer or basic authentication")
		}
		return "", "", nil
	case constant.AuthTypeBearer:
		if authToken == "" {
			return "", "", errors.New("Prometheus bearer authentication requires AuthToken")
		}
		if authUser != "" || authPass != "" {
			return "", "", errors.New("Prometheus bearer authentication must not include basic credentials")
		}
		return authType, authToken, nil
	case constant.AuthTypeBasic:
		if authUser == "" {
			return "", "", errors.New("Prometheus basic authentication requires AuthUser")
		}
		if authToken != "" {
			return "", "", errors.New("Prometheus basic authentication must not include AuthToken")
		}
		return authType, "", nil
	default:
		return "", "", fmt.Errorf("unsupported Prometheus AuthType %q", authType)
	}
}
