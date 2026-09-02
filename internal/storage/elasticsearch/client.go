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
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

// tlsOptions controls server certificate verification for the ES transport.
type tlsOptions struct {
	// InsecureSkipVerify disables certificate verification. Verification is
	// on by default: a man in the middle can otherwise capture the basic-auth
	// credentials sent on every request.
	InsecureSkipVerify bool
	// CABundle optionally points at a file with PEM-encoded CA certificates
	// used to verify servers that do not chain to the system roots.
	CABundle string
}

// newTransport is sized to keep TLS handshake cost off the hot path.
// Under FIPS, each fresh handshake spends several ms on RSA-PSS verification;
// a small idle pool turned bursty writes into per-request handshakes and
// dominated CPU. The idle/total caps below let concurrent writers reuse
// connections; MaxConnsPerHost bounds blast radius if ES slows down.
//
// ClientSessionCache enables TLS 1.3 PSK resumption: when the server (or an
// intermediate proxy) silently closes an idle connection, the next handshake
// reuses a ticket instead of doing full RSA-PSS verification.
func newTransport(opts tlsOptions) (http.RoundTripper, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: opts.InsecureSkipVerify, //nolint:gosec // opt-in via config
		ClientSessionCache: tls.NewLRUClientSessionCache(64),
	}

	if opts.CABundle != "" {
		pem, err := os.ReadFile(opts.CABundle)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle %q: %w", opts.CABundle, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA bundle %q contains no certificates", opts.CABundle)
		}
		tlsCfg.RootCAs = pool
	}

	return &http.Transport{
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
		TLSClientConfig: tlsCfg,
	}, nil
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
	if resp.Header.Get("X-Elastic-Product") == "" {
		resp.Header.Set("X-Elastic-Product", "Elasticsearch")
	}
	return resp, nil
}

// newCompatClient returns an *elasticsearch.Client that connects to ES v7 or
// ES v8 without any caller-side branching.
//
//   - ES v8:         native support.
//   - ES v7 ≥ 7.14: CompatibilityMode headers + native product header.
//   - ES v7 < 7.14: CompatibilityMode headers + injected product header.
//   - OpenSearch:    returns X-Elastic-Product natively; no separate client needed.
func newCompatClient(addresses []string, username, password string, opts tlsOptions) (*elasticsearch.Client, error) {
	transport, err := newTransport(opts)
	if err != nil {
		return nil, err
	}

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses:               addresses,
		Username:                username,
		Password:                password,
		EnableCompatibilityMode: true,
		Transport:               &productHeaderTransport{inner: transport},
		// Whole-batch retry: covers transport failures and 429/5xx returned for
		// the entire bulk request. Per-item failures inside a 200 response are
		// surfaced through BulkIndexerItem.OnFailure instead.
		RetryOnStatus: []int{429, 502, 503, 504},
		MaxRetries:    3,
		RetryBackoff: func(attempt int) time.Duration {
			return time.Duration(100<<attempt) * time.Millisecond
		},
	})
	if err != nil {
		return nil, fmt.Errorf("elasticsearch new client: %w", err)
	}

	res, err := client.Info()
	if err != nil {
		return nil, fmt.Errorf("elasticsearch client info: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, fmt.Errorf("elasticsearch client info: status %d", res.StatusCode)
	}
	return client, nil
}
