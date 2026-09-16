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

package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

const defaultNodeRequestTimeout = 10 * time.Second

// NodeRequestObserver observes one NodeClient request after response parsing and
// error classification. The duration includes generated-client setup, transport,
// and response parsing, but excludes the observer itself. The error is the same
// value returned to the caller.
//
// NodeClient invokes the observer synchronously and may invoke it concurrently.
// Implementations must return promptly and be safe for concurrent use. NodeClient
// does not recover observer panics. A nil observer disables observation. Argument
// validation that rejects a call before request execution does not invoke it.
type NodeRequestObserver func(operation string, duration time.Duration, err error)

// NodeConfig contains process-local Node client dependencies and policy. Target
// addresses are supplied per request so one client can contact multiple Node
// Agents. The bearer token may be empty when only public endpoints are called.
type NodeConfig struct {
	HTTPClient     *http.Client
	BearerToken    string
	RequestTimeout time.Duration
	Observe        NodeRequestObserver
}

func (c *NodeConfig) validate() error {
	if c == nil {
		return errors.New("create Node client: config is required")
	}
	if strings.ContainsAny(c.BearerToken, " \t\r\n") {
		return errors.New("create Node client: bearer token must not contain whitespace")
	}
	if c.RequestTimeout < 0 {
		return errors.New("create Node client: request timeout must not be negative")
	}
	return nil
}

// NodeClient sends one generated Node API request per method call.
type NodeClient struct {
	httpClient     *http.Client
	bearerToken    string
	requestTimeout time.Duration
	observe        NodeRequestObserver
}

// NewNode validates and snapshots Node client configuration.
func NewNode(config *NodeConfig) (*NodeClient, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultNodeRequestTimeout
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
	return &NodeClient{
		httpClient:     httpClient,
		bearerToken:    config.BearerToken,
		requestTimeout: requestTimeout,
		observe:        config.Observe,
	}, nil
}

func (c *NodeClient) generatedClient(serverURL string) (*nodeapi.Client, error) {
	options := []nodeapi.ClientOption{
		nodeapi.WithHTTPClient(c.httpClient),
	}
	if c.bearerToken != "" {
		options = append(options, nodeapi.WithRequestEditorFn(func(
			_ context.Context,
			request *http.Request,
		) error {
			request.Header.Set("Authorization", "Bearer "+c.bearerToken)
			return nil
		}))
	}
	return nodeapi.NewClient(serverURL, options...)
}
