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
	"strings"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

// defaultTransport is sized to keep TLS handshake cost off the hot path.
// Under FIPS, each fresh handshake spends several ms on RSA-PSS verification;
// a small idle pool turned bursty writes into per-request handshakes and
// dominated CPU. The idle/total caps below let concurrent writers reuse
// connections; MaxConnsPerHost bounds blast radius if ES slows down.
var defaultTransport http.RoundTripper = &http.Transport{
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   100,
	MaxConnsPerHost:       200,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 10 * time.Second,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // #nosec G402
	},
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
func newCompatClient(addresses []string, username, password string) (*elasticsearch.Client, error) {
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses:               addresses,
		Username:                username,
		Password:                password,
		EnableCompatibilityMode: true,
		Transport:               &productHeaderTransport{inner: defaultTransport},
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
