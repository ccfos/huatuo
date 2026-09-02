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
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The basic-auth credentials travel on every request, so the transport must
// verify server certificates by default and only trust an explicitly
// configured CA bundle.
func TestNewTransportServerVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest() error=%v", err)
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, encodeCertPEM(server.Certificate()), 0o600); err != nil {
		t.Fatalf("WriteFile(CA bundle) error=%v", err)
	}

	tests := []struct {
		name    string
		opts    tlsOptions
		wantErr bool
	}{
		{
			name:    "verification on without CA rejects the self-signed server",
			opts:    tlsOptions{},
			wantErr: true,
		},
		{
			name:    "verification on with the server CA succeeds",
			opts:    tlsOptions{CABundle: caFile},
			wantErr: false,
		},
		{
			name:    "verification off succeeds",
			opts:    tlsOptions{InsecureSkipVerify: true},
			wantErr: false,
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			transport, err := newTransport(tests[i].opts)
			if err != nil {
				t.Fatalf("newTransport() error=%v", err)
			}

			client := &http.Client{Transport: transport}
			resp, err := client.Do(request.Clone(request.Context()))
			if tests[i].wantErr {
				if err == nil {
					resp.Body.Close()
					t.Fatal("request succeeded, want certificate verification failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("request error=%v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status=%d, want %d", resp.StatusCode, http.StatusOK)
			}
		})
	}
}

func TestNewTransportInvalidCABundle(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pem")
	if _, err := newTransport(tlsOptions{CABundle: missing}); err == nil {
		t.Error("newTransport() with missing CA bundle error=nil, want error")
	}

	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("WriteFile() error=%v", err)
	}
	if _, err := newTransport(tlsOptions{CABundle: empty}); err == nil {
		t.Error("newTransport() with garbage CA bundle error=nil, want error")
	}
}

func encodeCertPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}
