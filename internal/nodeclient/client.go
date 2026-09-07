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
	defaultRequestTimeout = 10 * time.Second
	maxSuccessBodyBytes   = 1 << 20
	maxErrorBodyBytes     = 8 << 10
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
	// Start and Stop are not safely retryable when the response is lost.
	startedAt := time.Now()
	defer func() {
		if c.observe != nil {
			c.observe(operationName, time.Since(startedAt), returnedErr)
		}
	}()

	generated, err := c.generatedClient(host)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	response, err := send(requestCtx, generated)
	if err != nil {
		return nil, wrapError(&Error{
			Code:    ErrorCodeClientTransport,
			Message: operationName + " Node API request",
		}, err)
	}
	return parseResponse(response, requestID, successMode)
}

func (c *Client) generatedClient(host string) (*nodeapi.Client, error) {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/?#") {
		return nil, &Error{
			Code:    ErrorCodeClientInvalidArgument,
			Message: fmt.Sprintf("invalid Node host %q", host),
		}
	}
	serverURL := (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(c.port)),
	}).String()
	generated, err := nodeapi.NewClient(
		serverURL,
		nodeapi.WithHTTPClient(c.httpClient),
		nodeapi.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+c.bearerToken)
			return nil
		}),
	)
	if err != nil {
		return nil, wrapError(&Error{
			Code:    ErrorCodeClientInvalidArgument,
			Message: "create generated Node API client",
		}, err)
	}
	return generated, nil
}

func parseResponse(
	response *http.Response,
	requestID string,
	successMode successResponseMode,
) (*nodeapi.Operation, error) {
	if response == nil {
		return nil, &Error{
			Code:    ErrorCodeClientProtocol,
			Message: "Node API returned a nil response",
		}
	}
	if response.Body == nil {
		return nil, &Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "Node API returned a nil response body",
		}
	}

	limit := int64(maxErrorBodyBytes)
	if response.StatusCode == http.StatusOK ||
		successMode == successResponseOKOrAccepted && response.StatusCode == http.StatusAccepted {
		limit = maxSuccessBodyBytes
	}
	body, readErr := readBody(response.Body, limit)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, wrapError(&Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "read Node API response",
		}, readErr)
	}
	if closeErr != nil {
		return nil, wrapError(&Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientTransport,
			Message:    "close Node API response",
		}, closeErr)
	}
	switch response.StatusCode {
	case http.StatusOK:
		return parseOperation(response.StatusCode, body, requestID)
	case http.StatusAccepted:
		if successMode == successResponseOKOrAccepted {
			return parseOperation(response.StatusCode, body, requestID)
		}
		return nil, &Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "unexpected HTTP 202 success response",
		}
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

func parseOperation(statusCode int, body []byte, requestID string) (*nodeapi.Operation, error) {
	var envelope nodeapi.OperationResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, wrapError(&Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "decode operation response",
		}, err)
	}
	operation := envelope.Data
	if operation.RequestID != requestID {
		return nil, &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message: fmt.Sprintf(
				"response request ID %q does not match %q",
				operation.RequestID,
				requestID,
			),
		}
	}
	if !operation.Status.Valid() {
		return nil, &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("unsupported operation status %q", operation.Status),
		}
	}
	if operation.Status == nodeapi.OperationStatusTerminal {
		if operation.Terminal == nil || !operation.Terminal.Outcome.Valid() {
			return nil, &Error{
				StatusCode: statusCode,
				Code:       ErrorCodeClientProtocol,
				Message:    "terminal operation has an invalid outcome",
			}
		}
		if operation.Terminal.Outcome == nodeapi.OperationOutcomeFailed &&
			(operation.Terminal.Reason == nil || *operation.Terminal.Reason == "") {
			return nil, &Error{
				StatusCode: statusCode,
				Code:       ErrorCodeClientProtocol,
				Message:    "failed operation has no reason",
			}
		}
	} else if operation.Terminal != nil {
		return nil, &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("operation status %q contains terminal details", operation.Status),
		}
	}
	return &operation, nil
}

func parseError(statusCode int, body []byte) error {
	var envelope apiv1.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return wrapError(&Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "decode Node API error response",
		}, err)
	}
	code := envelope.Error.Code
	expectedStatus, ok := nodeapi.HTTPStatusForErrorCode(code)
	if !ok {
		return &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("unknown Node error code %q", code),
		}
	}
	if expectedStatus != statusCode {
		return &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message: fmt.Sprintf(
				"Node error code %q requires HTTP %d",
				code,
				expectedStatus,
			),
		}
	}
	if envelope.Error.Message == "" {
		return &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("Node error code %q has an empty message", code),
		}
	}
	return &Error{
		StatusCode: statusCode,
		Code:       code,
		Message:    envelope.Error.Message,
	}
}
