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

type tlsOptions struct {
	CAFile             string
	CertFile           string
	KeyFile            string
	InsecureSkipVerify *bool
}

// newDefaultTransport is sized to keep TLS handshake cost off the hot path.
// Under FIPS, each fresh handshake spends several ms on RSA-PSS verification;
// a small idle pool turned bursty writes into per-request handshakes and
// dominated CPU. The idle/total caps below let concurrent writers reuse
// connections; MaxConnsPerHost bounds blast radius if ES slows down.
//
// ClientSessionCache enables TLS 1.3 PSK resumption: when the server (or an
// intermediate proxy) silently closes an idle connection, the next handshake
// reuses a ticket instead of doing full RSA-PSS verification.
func newDefaultTransport(tlsConfig *tls.Config) http.RoundTripper {
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
		TLSClientConfig: tlsConfig,
	}
}

func newTLSConfig(opts tlsOptions) (*tls.Config, error) {
	insecureSkipVerify := true
	if opts.InsecureSkipVerify != nil {
		insecureSkipVerify = *opts.InsecureSkipVerify
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify, // #nosec G402
		ClientSessionCache: tls.NewLRUClientSessionCache(64),
	}

	if opts.CAFile != "" {
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			rootCAs = x509.NewCertPool()
		}

		caPEM, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read elasticsearch ca file %q: %w", opts.CAFile, err)
		}
		if ok := rootCAs.AppendCertsFromPEM(caPEM); !ok {
			return nil, fmt.Errorf("load elasticsearch ca file %q: no certificate found", opts.CAFile)
		}
		tlsConfig.RootCAs = rootCAs
	}

	if opts.CertFile != "" || opts.KeyFile != "" {
		if opts.CertFile == "" || opts.KeyFile == "" {
			return nil, fmt.Errorf("elasticsearch client cert file and key file must be configured together")
		}

		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load elasticsearch client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
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
func newCompatClient(cfg *Config) (*elasticsearch.Client, error) {
	tlsConfig, err := newTLSConfig(tlsOptions{
		CAFile:             cfg.CAFile,
		CertFile:           cfg.CertFile,
		KeyFile:            cfg.KeyFile,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
	if err != nil {
		return nil, err
	}

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses:               cfg.Addresses,
		Username:                cfg.Username,
		Password:                cfg.Password,
		EnableCompatibilityMode: true,
		Transport: &productHeaderTransport{
			inner: newDefaultTransport(tlsConfig),
		},
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
