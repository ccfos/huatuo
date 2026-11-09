// Copyright 2025, 2026 The HuaTuo Authors
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

package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubeletDefaultConfigPathsIncludesEtcAndHostEtc(t *testing.T) {
	// Verify default paths include both /etc and /host/etc variants.
	// This addresses issue #75 where AWS EKS users had kubelet config at
	// /etc/kubernetes/kubelet/config.json which was not in the search path.
	foundEtc := false
	foundHostEtc := false
	for _, p := range kubeletDefaultConfigPath {
		if p == "/etc/kubernetes/kubelet/config.json" {
			foundEtc = true
		}
		if p == "/host/etc/kubernetes/kubelet/config.json" {
			foundHostEtc = true
		}
	}
	if !foundEtc {
		t.Error("kubeletDefaultConfigPath should include /etc/kubernetes/kubelet/config.json")
	}
	if !foundHostEtc {
		t.Error("kubeletDefaultConfigPath should include /host/etc/kubernetes/kubelet/config.json")
	}
}

func TestKubeletDefaultConfigPathsMinimum(t *testing.T) {
	// Verify that the search paths include common kubelet config locations
	expectedMin := 3
	if len(kubeletDefaultConfigPath) < expectedMin {
		t.Errorf("kubeletDefaultConfigPath has only %d entries, expected at least %d",
			len(kubeletDefaultConfigPath), expectedMin)
	}
}

func TestKubeletConfigurationFallback(t *testing.T) {
	previous := kubeletDefaultConfigPath
	t.Cleanup(func() { kubeletDefaultConfigPath = previous })
	root := t.TempDir()
	malformed := filepath.Join(root, "malformed.yaml")
	valid := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(malformed, []byte("[unterminated"), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(valid, []byte("cgroupDriver: systemd\ncontainerRuntimeEndpoint: unix:///run/containerd/local.sock\n"), 0o600); err != nil {
		t.Error(err)
		return
	}
	kubeletDefaultConfigPath = []string{filepath.Join(root, "missing.yaml"), malformed, valid}
	for _, test := range []struct {
		name     string
		status   int
		body     string
		driver   string
		endpoint string
	}{
		{name: "remote wins", status: http.StatusOK, body: `{"kubeletconfig":{"cgroupDriver":"cgroupfs","containerRuntimeEndpoint":"unix:///run/containerd/remote.sock"}}`, driver: "cgroupfs", endpoint: "unix:///run/containerd/remote.sock"},
		{name: "remote unavailable", status: http.StatusForbidden, body: "forbidden", driver: "systemd", endpoint: "unix:///run/containerd/local.sock"},
		{name: "remote malformed", status: http.StatusOK, body: "{", driver: "systemd", endpoint: "unix:///run/containerd/local.sock"},
		{name: "remote empty preserves defaults", status: http.StatusOK, body: `{"kubeletconfig":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Kubelet{client: &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}}
			config, err := client.MustConfiguration(t.Context())
			if err != nil || config.CgroupDriver != test.driver || config.ContainerRuntimeEndpoint != test.endpoint {
				t.Errorf("configuration=%+v err=%v", config, err)
			}
		})
	}
}

func TestKubeletConfigurationPanicsOnMissingConfig(t *testing.T) {
	previous := kubeletDefaultConfigPath
	t.Cleanup(func() { kubeletDefaultConfigPath = previous })
	kubeletDefaultConfigPath = []string{filepath.Join(t.TempDir(), "missing.yaml")}
	client := &Kubelet{client: &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("configz unavailable")
	})}}
	defer func() {
		value := recover()
		if value == nil || !strings.Contains(fmt.Sprint(value), "we cannot find any cgroup driver of kubelet") {
			t.Errorf("missing configuration panic=%v", value)
		}
	}()
	_, _ = client.MustConfiguration(t.Context())
}

func TestKubeletConfigurationCancellationDoesNotPanic(t *testing.T) {
	previous := kubeletDefaultConfigPath
	t.Cleanup(func() { kubeletDefaultConfigPath = previous })
	kubeletDefaultConfigPath = []string{filepath.Join(t.TempDir(), "missing.yaml")}
	ctx, cancel := context.WithCancel(t.Context())
	client := &Kubelet{client: &http.Client{Transport: kubeletRoundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, ctx.Err()
	})}}
	defer cancel()
	if _, err := client.MustConfiguration(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("configuration cancellation=%v", err)
	}
}
