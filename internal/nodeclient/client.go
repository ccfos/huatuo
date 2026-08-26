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

// Package nodeclient adapts the generated Node API client for Apiserver use.
package nodeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
)

const (
	defaultPort           = 19704
	defaultRequestTimeout = 10 * time.Second
	maxSuccessBodyBytes   = 1 << 20
	maxErrorBodyBytes     = 8 << 10
)

var (
	// ErrInvalidArgument indicates that a call cannot produce a valid request.
	ErrInvalidArgument = errors.New("node client: invalid argument")
	// ErrProtocol indicates that the Node response violates the API contract.
	ErrProtocol = errors.New("node client: protocol error")
)

// RequestObserver records one completed Node API call.
type RequestObserver func(operation string, duration time.Duration, err error)

// Config contains process-local Node client dependencies and policy.
type Config struct {
	HTTPClient     *http.Client
	Port           int
	BearerToken    string
	RequestTimeout time.Duration
	Observe        RequestObserver
}

// Error is a stable error response returned by a Node API.
type Error struct {
	StatusCode int
	Code       apiv1.ErrorCode
	Message    string
}

// Error formats the stable response without exposing its raw body.
func (e *Error) Error() string {
	if e == nil {
		return "node API error"
	}
	return fmt.Sprintf("node API returned HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// Client sends one generated Node API request per method call.
type Client struct {
	httpClient     *http.Client
	port           int
	bearerToken    string
	requestTimeout time.Duration
	observe        RequestObserver
}

// New validates and snapshots Node client configuration.
func New(config *Config) (*Client, error) {
	if config == nil {
		return nil, errors.New("create Node client: config is required")
	}
	if config.BearerToken == "" {
		return nil, errors.New("create Node client: bearer token is required")
	}
	if strings.ContainsAny(config.BearerToken, " \t\r\n") {
		return nil, errors.New("create Node client: bearer token must not contain whitespace")
	}
	port := config.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("create Node client: port %d is outside 1..65535", port)
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	if requestTimeout < 0 {
		return nil, errors.New("create Node client: request timeout must not be negative")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		cloned := *httpClient
		httpClient = &cloned
	}
	// Redirects would violate the one-call/one-request dispatch contract.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		httpClient:     httpClient,
		port:           port,
		bearerToken:    config.BearerToken,
		requestTimeout: requestTimeout,
		observe:        config.Observe,
	}, nil
}

type sendRequest func(context.Context, *nodeapi.Client) (*http.Response, error)

type successResponseMode uint8

const (
	successResponseOK successResponseMode = iota
	successResponseOKOrAccepted
)

func (c *Client) execute(
	ctx context.Context,
	host string,
	operationName string,
	requestID string,
	successMode successResponseMode,
	send sendRequest,
) (result *nodeapi.Operation, returnedErr error) {
	startedAt := time.Now()
	defer func() {
		if c.observe != nil {
			c.observe(operationName, time.Since(startedAt), returnedErr)
		}
	}()

	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalidArgument)
	}
	if requestID == "" {
		return nil, fmt.Errorf("%w: request ID is required", ErrInvalidArgument)
	}
	generated, err := c.generatedClient(host)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	response, err := send(requestCtx, generated)
	if err != nil {
		return nil, fmt.Errorf("%s Node API request: %w", operationName, err)
	}
	return parseResponse(response, requestID, successMode)
}

func (c *Client) generatedClient(host string) (*nodeapi.Client, error) {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/?#") {
		return nil, fmt.Errorf("%w: invalid Node host %q", ErrInvalidArgument, host)
	}
	serverURL := (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(c.port)),
	}).String()
	return nodeapi.NewClient(
		serverURL,
		nodeapi.WithHTTPClient(c.httpClient),
		nodeapi.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+c.bearerToken)
			return nil
		}),
	)
}

func parseResponse(
	response *http.Response,
	requestID string,
	successMode successResponseMode,
) (*nodeapi.Operation, error) {
	if response == nil {
		return nil, fmt.Errorf("%w: Node API returned a nil response", ErrProtocol)
	}
	if response.Body == nil {
		return nil, fmt.Errorf("%w: Node API returned a nil response body", ErrProtocol)
	}

	limit := int64(maxErrorBodyBytes)
	if response.StatusCode == http.StatusOK ||
		successMode == successResponseOKOrAccepted && response.StatusCode == http.StatusAccepted {
		limit = maxSuccessBodyBytes
	}
	body, readErr := readBody(response.Body, limit)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("%w: read Node API response: %w", ErrProtocol, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: close Node API response: %w", ErrProtocol, closeErr)
	}
	contentType := response.Header.Get("Content-Type")
	mediaType, _, mediaTypeErr := mime.ParseMediaType(contentType)
	if mediaTypeErr != nil || mediaType != "application/json" {
		return nil, fmt.Errorf(
			"%w: Node API returned content type %q",
			ErrProtocol,
			contentType,
		)
	}

	switch response.StatusCode {
	case http.StatusOK:
		return parseOperation(body, requestID)
	case http.StatusAccepted:
		if successMode == successResponseOKOrAccepted {
			return parseOperation(body, requestID)
		}
		return nil, fmt.Errorf("%w: unexpected HTTP 202 success response", ErrProtocol)
	default:
		return nil, parseError(response.StatusCode, body)
	}
}

func readBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func parseOperation(body []byte, requestID string) (*nodeapi.Operation, error) {
	var envelope nodeapi.OperationResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("%w: decode operation response: %w", ErrProtocol, err)
	}
	operation := envelope.Data
	if operation.RequestID != requestID {
		return nil, fmt.Errorf(
			"%w: response request ID %q does not match %q",
			ErrProtocol,
			operation.RequestID,
			requestID,
		)
	}
	if !operation.Status.Valid() {
		return nil, fmt.Errorf("%w: unsupported operation status %q", ErrProtocol, operation.Status)
	}
	if operation.Status == nodeapi.OperationStatusFailed {
		if operation.Failure == nil || !isOperationFailureCode(operation.Failure.Code) {
			return nil, fmt.Errorf("%w: failed operation has an invalid failure", ErrProtocol)
		}
	} else if operation.Failure != nil {
		return nil, fmt.Errorf(
			"%w: operation status %q contains a failure",
			ErrProtocol,
			operation.Status,
		)
	}
	return &operation, nil
}

func parseError(statusCode int, body []byte) error {
	var envelope apiv1.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%w: decode HTTP %d error response: %w", ErrProtocol, statusCode, err)
	}
	code := envelope.Error.Code
	expectedStatus, ok := nodeapi.HTTPStatusForErrorCode(code)
	if !ok {
		return fmt.Errorf("%w: unknown Node error code %q", ErrProtocol, code)
	}
	if expectedStatus != statusCode {
		return fmt.Errorf(
			"%w: Node error code %q requires HTTP %d, got HTTP %d",
			ErrProtocol,
			code,
			expectedStatus,
			statusCode,
		)
	}
	if envelope.Error.Message == "" {
		return fmt.Errorf("%w: Node error code %q has an empty message", ErrProtocol, code)
	}
	return &Error{
		StatusCode: statusCode,
		Code:       code,
		Message:    envelope.Error.Message,
	}
}

func isOperationFailureCode(code apiv1.ErrorCode) bool {
	switch code {
	case nodeapi.ErrorCodeExecutionStartFailed,
		nodeapi.ErrorCodeLaunchTimeout,
		nodeapi.ErrorCodeExecutionFailed,
		nodeapi.ErrorCodeExecutionStopFailed,
		nodeapi.ErrorCodeFinalizationFailed,
		nodeapi.ErrorCodeFinalizationTimeout:
		return true
	default:
		return false
	}
}
