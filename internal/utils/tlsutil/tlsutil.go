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

// Package tlsutil helps build TLS client configs for the agent's outbound
// endpoints (Elasticsearch, kubelet), whose certificates may chain to an
// internal CA instead of the system roots.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// AppendCABundle reads a PEM-encoded certificate bundle and sets it as the
// root CA pool of cfg. An empty path leaves cfg unchanged.
func AppendCABundle(cfg *tls.Config, caBundle string) error {
	if caBundle == "" {
		return nil
	}

	pem, err := os.ReadFile(caBundle)
	if err != nil {
		return fmt.Errorf("read CA bundle %q: %w", caBundle, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("CA bundle %q contains no certificates", caBundle)
	}
	cfg.RootCAs = pool
	return nil
}
