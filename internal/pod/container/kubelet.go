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

package container

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/ccfos/huatuo/internal/log"
)

const (
	kubeletOversizedResponseWarningInterval = 30 * time.Minute
	maxKubeletResponseBodyBytes             = 128 << 20
	maxKubeletErrorBodyBytes                = 8 << 10
)

var kubeletOversizedResponseWarning = &rate.Sometimes{Interval: kubeletOversizedResponseWarningInterval}

// KubeletConfiguration contains the runtime settings needed for discovery.
type KubeletConfiguration struct {
	// cgroupDriver is the driver kubelet uses to manipulate CGroups on the host (cgroupfs
	// or systemd).
	// Default: "cgroupfs"
	// +optional
	CgroupDriver string `json:"cgroupDriver,omitempty"`
	// ContainerRuntimeEndpoint is the endpoint of container runtime.
	// Unix Domain Sockets are supported on Linux, while npipe and tcp endpoints are supported on Windows.
	// Examples:'unix:///path/to/runtime.sock', 'npipe:////./pipe/runtime'
	ContainerRuntimeEndpoint string `json:"containerRuntimeEndpoint"`
}

type kubeletConfigz struct {
	Kubeletconfig KubeletConfiguration `json:"kubeletconfig"`
}

// FetchPodList reads one bounded kubelet response under the discovery deadline.
func FetchPodList(ctx context.Context, client *http.Client, url string) (corev1.PodList, error) {
	podList := corev1.PodList{}
	body, err := httpRequestContext(ctx, client, url)
	if err != nil {
		return podList, err
	}

	if err := json.Unmarshal(body, &podList); err != nil {
		return podList, fmt.Errorf(
			"http: %s, Unmarshal: %w, body: %s",
			url,
			err,
			requestErrorBody(body),
		)
	}

	return podList, nil
}

// FetchKubeletConfig reads the kubelet configz endpoint.
func FetchKubeletConfig(
	ctx context.Context,
	client *http.Client,
	kubeletConfigURL string,
) (KubeletConfiguration, error) {
	empty := KubeletConfiguration{}

	body, err := httpRequestContext(ctx, client, kubeletConfigURL)
	if err != nil {
		return empty, err
	}

	config := kubeletConfigz{}
	if err := json.Unmarshal(body, &config); err != nil {
		return empty, fmt.Errorf(
			"http: %s, Unmarshal: %w, body: %s",
			kubeletConfigURL,
			err,
			requestErrorBody(body),
		)
	}

	return config.Kubeletconfig, nil
}

func httpRequestContext(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _, err := requestLimitedBody(resp.Body, maxKubeletErrorBodyBytes)
		if err != nil {
			return nil, fmt.Errorf("http: %s, read body: %w", url, err)
		}
		return nil, fmt.Errorf(
			"http: %s, status: %d, body: %s", url,
			resp.StatusCode,
			requestErrorBody(body),
		)
	}

	if resp.ContentLength > maxKubeletResponseBodyBytes {
		err := fmt.Errorf(
			"http: %s, response body declares %d bytes, limit is %d bytes",
			url,
			resp.ContentLength,
			maxKubeletResponseBodyBytes,
		)
		kubeletOversizedResponseWarning.Do(func() {
			log.WithError(err).
				WithField("url", url).
				WithField("declared_size_bytes", resp.ContentLength).
				WithField("limit_bytes", maxKubeletResponseBodyBytes).
				Warn("rejecting oversized kubelet response")
		})
		return nil, err
	}

	// ContentLength covers declared-size responses; the stream check below covers
	// unknown, chunked, decompressed, and inaccurate length declarations.
	body, truncated, err := requestLimitedBody(resp.Body, maxKubeletResponseBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("http: %s, read body: %w", url, err)
	}
	if truncated {
		err := fmt.Errorf(
			"http: %s, response body exceeds %d bytes",
			url,
			maxKubeletResponseBodyBytes,
		)
		kubeletOversizedResponseWarning.Do(func() {
			log.WithError(err).
				WithField("url", url).
				WithField("observed_size_bytes", maxKubeletResponseBodyBytes+1).
				WithField("limit_bytes", maxKubeletResponseBodyBytes).
				Warn("rejecting oversized kubelet response")
		})
		return nil, err
	}

	return body, nil
}

func requestLimitedBody(body io.Reader, limit int64) ([]byte, bool, error) {
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, false, fmt.Errorf("invalid response byte limit %d", limit)
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= limit {
		return data, false, nil
	}
	// Preserve the probe byte so error formatting can identify truncation without
	// carrying separate state.
	return data, true, nil
}

func requestErrorBody(body []byte) string {
	truncated := len(body) > maxKubeletErrorBodyBytes
	if len(body) > maxKubeletErrorBodyBytes {
		body = body[:maxKubeletErrorBodyBytes]
	}
	message := strings.TrimSpace(string(body))
	if truncated {
		message += fmt.Sprintf("... [truncated after %d bytes]", maxKubeletErrorBodyBytes)
	}
	return message
}

// KubeletOptions selects the existing read-only or certificate-authenticated endpoint.
type KubeletOptions struct {
	ReadOnlyPort   uint32
	AuthorizedPort uint32
	ClientCertPath string
}

// Kubelet owns connection selection for one discovery producer. Calls are serial.
type Kubelet struct {
	options KubeletOptions
	client  *http.Client
	podURL  string
}

func NewKubelet(options KubeletOptions) (*Kubelet, error) {
	if options.ReadOnlyPort == 0 && options.AuthorizedPort == 0 {
		return nil, fmt.Errorf("configure a kubelet read-only or authorized port")
	}
	if options.ReadOnlyPort == 0 && options.ClientCertPath == "" {
		return nil, fmt.Errorf("authorized kubelet port requires a client certificate")
	}
	return &Kubelet{options: options}, nil
}

// Initialize probes the read-only endpoint before the existing HTTPS fallback.
// Failed attempts publish no client and can be retried by the Manager.
func (k *Kubelet) Initialize(ctx context.Context) error {
	if k.client != nil {
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/pods", k.options.ReadOnlyPort)
	if _, err := FetchPodList(ctx, client, url); err == nil {
		k.client, k.podURL = client, url
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	certs := strings.Split(k.options.ClientCertPath, ",")
	certPath := strings.TrimSpace(certs[0])
	keyPath := certPath
	if len(certs) > 1 {
		keyPath = strings.TrimSpace(certs[1])
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("loading client key pair [%s,%s]: %w", certPath, keyPath, err)
	}
	client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true, // #nosec G402 -- preserve existing kubelet authentication.
		}},
	}
	url = fmt.Sprintf("https://127.0.0.1:%d/pods", k.options.AuthorizedPort)
	if _, err := FetchPodList(ctx, client, url); err != nil {
		client.CloseIdleConnections()
		return fmt.Errorf("podlist https: %w", err)
	}
	k.client, k.podURL = client, url
	return nil
}

func (k *Kubelet) Pods(ctx context.Context) (corev1.PodList, error) {
	if k.client == nil {
		return corev1.PodList{}, fmt.Errorf("initialize kubelet client before discovery")
	}
	return FetchPodList(ctx, k.client, k.podURL)
}

func (k *Kubelet) remoteConfiguration(ctx context.Context) (KubeletConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return KubeletConfiguration{}, err
	}
	if k.client == nil {
		return KubeletConfiguration{}, fmt.Errorf("initialize kubelet client before reading configuration")
	}
	return FetchKubeletConfig(ctx, k.client, fmt.Sprintf("https://127.0.0.1:%d/configz", k.options.AuthorizedPort))
}

var kubeletDefaultConfigPath = []string{
	"/var/lib/kubelet/config.yaml",
	"/var/lib/kubelet/ack-managed-config.yaml",
	"/etc/kubernetes/kubelet/config.json",
	"/host/etc/kubernetes/kubelet/config.json",
}

func kubeletConfigFileDefault() (KubeletConfiguration, error) {
	empty := KubeletConfiguration{}

	for _, name := range kubeletDefaultConfigPath {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}

		config := KubeletConfiguration{}
		if err := yaml.Unmarshal(data, &config); err != nil {
			continue
		}

		return config, nil
	}

	return empty, fmt.Errorf("not found kubelet config")
}

// MustConfiguration preserves fatal startup behavior when both configz and local
// configuration are unavailable. Cancellation must not trigger that fallback.
func (k *Kubelet) MustConfiguration(ctx context.Context) (KubeletConfiguration, error) {
	config, err := k.remoteConfiguration(ctx)
	if err == nil {
		return config, nil
	}
	if err := ctx.Err(); err != nil {
		return KubeletConfiguration{}, err
	}
	log.Debugf("kubelet config port is not available, try to read config files: %v", kubeletDefaultConfigPath)
	config, err = kubeletConfigFileDefault()
	if err != nil {
		panic(fmt.Sprintf("we cannot find any cgroup driver of kubelet after requesting configz and default files: %v", err))
	}
	return config, nil
}
