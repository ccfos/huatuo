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

//go:build integration

package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/runmode"
)

// TestConfiguredWorkloadDiscovery 使用真实 HTTPS kubelet fixture 验证配置经生产
// 启动入口传给 Manager：自定义周期触发下一轮，整轮超时取消请求，停止可回收资源。
func TestConfiguredWorkloadDiscovery(t *testing.T) {
	var calls atomic.Int32
	requests := make(chan time.Time, 4)
	canceled := make(chan time.Duration, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/configz" {
			_, _ = w.Write([]byte(`{"kubeletconfig":{"cgroupDriver":"systemd"}}`))
			return
		}
		started := time.Now()
		call := calls.Add(1)
		// Exclude the connection probe from the discovery interval measurement.
		if call > 1 {
			select {
			case requests <- started:
			case <-r.Context().Done():
				return
			}
		}
		if call <= 2 {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		<-r.Context().Done()
		canceled <- time.Since(started)
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Error(err)
		return
	}
	certificate := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Error(err)
		return
	}
	certPath := filepath.Join(t.TempDir(), "kubelet-client.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Error(err)
		return
	}
	path := filepath.Join(t.TempDir(), "discovery.conf")
	body := fmt.Sprintf(`[HTTPServer.Auth]
BearerToken="discovery-test-token"
[Pod]
Source="container"
SyncIntervalSeconds=1
DiscoveryTimeoutSeconds=1
KubeletReadOnlyPort=0
KubeletAuthorizedPort=%s
KubeletClientCertPath=%q
`, port, certPath)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := config.Load(path); err != nil {
		t.Error(err)
		return
	}
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = t.TempDir()
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	cleanup, err := setupPodManager(&Daemon{opts: &Options{Mode: runmode.Baremetal}})
	if err != nil || cleanup == nil {
		t.Errorf("setupPodManager()=%v, cleanup present=%t", err, cleanup != nil)
		return
	}
	t.Cleanup(func() {
		if err := cleanup(t.Context()); err != nil {
			t.Error(err)
		}
	})
	select {
	case elapsed := <-canceled:
		if elapsed < 750*time.Millisecond || elapsed > 3*time.Second {
			t.Errorf("configured one-second discovery timeout took %v", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Error("configured interval and timeout did not produce a canceled second query")
		return
	}
	first, second := <-requests, <-requests
	if interval := second.Sub(first); interval < 750*time.Millisecond || interval > 3*time.Second {
		t.Errorf("configured one-second sync interval took %v", interval)
	}
}
