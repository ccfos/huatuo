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

// Package nodeclient adapts the generated Node API client for Huatuo components.
package nodeclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
)

const defaultRequestTimeout = 10 * time.Second

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

func (c *Config) validate() error {
	if c == nil {
		return errors.New("create Node client: config is required")
	}
	if c.BearerToken == "" {
		return errors.New("create Node client: bearer token is required")
	}
	if strings.ContainsAny(c.BearerToken, " \t\r\n") {
		return errors.New("create Node client: bearer token must not contain whitespace")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("create Node client: port %d is outside 1..65535", c.Port)
	}
	if c.RequestTimeout < 0 {
		return errors.New("create Node client: request timeout must not be negative")
	}
	return nil
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
	if err := config.validate(); err != nil {
		return nil, err
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
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
		port:           config.Port,
		bearerToken:    config.BearerToken,
		requestTimeout: requestTimeout,
		observe:        config.Observe,
	}, nil
}

type sendRequest func(context.Context, *nodeapi.Client) (*http.Response, error)

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
	return parseOperationResponse(response, requestID, successMode)
}

func (c *Client) generatedClient(host string) (*nodeapi.Client, error) {
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
