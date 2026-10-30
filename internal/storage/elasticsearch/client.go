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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

// defaultTransport is sized to keep TLS handshake cost off the hot path.
// Under FIPS, each fresh handshake spends several ms on RSA-PSS verification;
// a small idle pool turned bursty writes into per-request handshakes and
// dominated CPU. The idle/total caps below let concurrent writers reuse
// connections; MaxConnsPerHost bounds blast radius if ES slows down.
//
// ClientSessionCache enables TLS 1.3 PSK resumption: when the server (or an
// intermediate proxy) silently closes an idle connection, the next handshake
// reuses a ticket instead of doing full RSA-PSS verification.
var defaultTransport http.RoundTripper = newTransport(nil)

func newTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:               proxy,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     200,
		// Keep below typical server-side idle timeouts (ES/nginx/LB ~60s) so the
		// client closes first. If the server closes a connection we still hold,
		// the next request races into a stale conn and triggers a fresh handshake.
		IdleConnTimeout:       50 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402
			ClientSessionCache: tls.NewLRUClientSessionCache(64),
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// productHeaderTransport injects X-Elastic-Product: Elasticsearch into
// responses that lack it. Required for ES v7 < 7.14 which pre-dates the header.
type productHeaderTransport struct {
	inner http.RoundTripper
}

func (t *productHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// OpenSearch 2.x rejects the ES v8 vendor media type with 406.
	// ES v8 requires Content-Type and Accept to be consistent — rewrite both.
	content := req.Header.Get("Content-Type")
	accept := req.Header.Get("Accept")
	if strings.Contains(content, "application/vnd.elasticsearch") || strings.Contains(accept, "application/vnd.elasticsearch") {
		req = req.Clone(req.Context())
		if strings.Contains(content, "application/vnd.elasticsearch") {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	if resp.Header.Get("X-Elastic-Product") == "" {
		resp.Header.Set("X-Elastic-Product", "Elasticsearch")
	}
	return resp, nil
}

// ReadClientConfig keeps request policy separate from SDK and connection setup.
type ReadClientConfig struct {
	elasticsearch.Config
	// WrapTransport adds caller-owned guards without taking ownership of the pool.
	WrapTransport func(http.RoundTripper) http.RoundTripper
}

// ReadClient exposes the SDK with an independently owned default connection pool.
// A caller-supplied Transport is borrowed and is never closed by this client.
type ReadClient struct {
	*elasticsearch.Client
	transport *http.Transport
}

// Close releases this client's idle connections, not in-flight requests or any
// borrowed transport. Callers must stop issuing requests before closing.
func (c *ReadClient) Close() error {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}

// NewReadClient creates a compatible client without probes, bulk workers, node
// discovery, or SDK retries. Read-only endpoint restrictions remain caller policy.
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
	config.DiscoverNodesOnStart = false
	config.DiscoverNodesInterval = 0
	var owned *http.Transport
	if config.Transport == nil {
		owned = newTransport(http.ProxyFromEnvironment)
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

// newCompatClient performs one SDK attempt; callers own retries and backoff.
func newCompatClient(cfg *elasticsearch.Config) (*elasticsearch.Client, error) {
	config := *cfg
	config.EnableCompatibilityMode = true
	transport := config.Transport
	if transport == nil {
		transport = defaultTransport
	}
	config.Transport = &productHeaderTransport{inner: transport}
	config.DisableRetry = true
	client, err := elasticsearch.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch new client: %w", err)
	}
	return client, nil
}
