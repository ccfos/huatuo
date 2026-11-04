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

package elasticsearch

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

// ReadClientConfig keeps request policy separate from SDK and connection setup.
type ReadClientConfig struct {
	elasticsearch.Config
	// WrapTransport adds caller-owned guards without taking ownership of the pool.
	WrapTransport func(http.RoundTripper) http.RoundTripper
}

// ReadClient owns its default connection pool but borrows a supplied Transport.
type ReadClient struct {
	*elasticsearch.Client
	transport *http.Transport
}

// Close releases idle connections after the caller stops issuing requests.
func (c *ReadClient) Close() error {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}

// NewReadClient creates an independent client without probes or SDK retries.
// Read-only endpoint restrictions remain caller policy.
func NewReadClient(cfg *ReadClientConfig) (*ReadClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("elasticsearch read client: configuration is required")
	}
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("elasticsearch read client: at least one address is required")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, fmt.Errorf("elasticsearch read client: username and password must be configured together")
	}
	for _, address := range cfg.Addresses {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("elasticsearch read client: address must be an absolute HTTP URL without credentials, query, or fragment")
		}
	}
	config := cfg.Config
	config.DisableRetry = true
	config.DiscoverNodesOnStart = false
	config.DiscoverNodesInterval = 0
	var owned *http.Transport
	if config.Transport == nil {
		owned = defaultTransport.(*http.Transport).Clone()
		owned.Proxy = http.ProxyFromEnvironment
		owned.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(64)
		owned.TLSHandshakeTimeout = 10 * time.Second
		owned.ExpectContinueTimeout = time.Second
		config.Transport = owned
	}
	if cfg.WrapTransport != nil {
		config.Transport = cfg.WrapTransport(config.Transport)
		if config.Transport == nil {
			if owned != nil {
				owned.CloseIdleConnections()
			}
			return nil, fmt.Errorf("elasticsearch read client: transport wrapper returned nil")
		}
	}
	client, err := newCompatClient(&config)
	if err != nil {
		if owned != nil {
			owned.CloseIdleConnections()
		}
		return nil, err
	}
	return &ReadClient{Client: client, transport: owned}, nil
}
