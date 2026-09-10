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

package pod

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateCAAndServerCert builds a CA and a server certificate for 127.0.0.1
// signed by it, mirroring a kubelet serving certificate.
func generateCAAndServerCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "huatuo-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{serverDER},
		PrivateKey:  serverKey,
	}, caDER
}

// The authorized kubelet client presents a client certificate, so the server
// side must be verified by default: an impostor can otherwise feed a fake pod
// list and poison container attribution.
func TestKubeletTLSConfigServerVerification(t *testing.T) {
	serverCert, caDER := generateCAAndServerCert(t)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert}}
	server.StartTLS()
	defer server.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("write CA bundle: %v", err)
	}

	tests := []struct {
		name     string
		ca       string
		insecure bool
		wantErr  bool
	}{
		{name: "verification on without CA rejects the kubelet cert", wantErr: true},
		{name: "verification on with cluster CA succeeds", ca: caFile},
		{name: "verification off succeeds", insecure: true},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			cfg, err := kubeletTLSConfig(&serverCert, tests[i].ca, tests[i].insecure)
			if err != nil {
				t.Fatalf("kubeletTLSConfig() error=%v", err)
			}

			client := &http.Client{
				Timeout:   5 * time.Second,
				Transport: &http.Transport{TLSClientConfig: cfg},
			}
			resp, err := client.Get(server.URL)
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
