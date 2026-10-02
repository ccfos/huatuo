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

package pod

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

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/utils/netutil"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const (
	kubeletReqTimeout                       = 5 * time.Second
	kubeletOversizedResponseWarningInterval = 30 * time.Minute
	maxKubeletResponseBodyBytes             = 128 << 20
	maxKubeletErrorBodyBytes                = 8 << 10
)

var (
	kubeletPodListRunningEnabled    = false
	kubeletPodListURL               string
	kubeletPodListClient            *http.Client
	kubeletPodCgroupDriver          = "cgroupfs"
	kubeletRuntimeEndpoint          = "unix:///run/containerd/containerd.sock"
	kubeletOversizedResponseWarning = &rate.Sometimes{
		Interval: kubeletOversizedResponseWarningInterval,
	}
	kubeletDefaultConfigPath = []string{
		"/var/lib/kubelet/config.yaml",
		"/var/lib/kubelet/ack-managed-config.yaml",
		"/etc/kubernetes/kubelet/config.json",
		"/host/etc/kubernetes/kubelet/config.json",
	}
)

type kubeletConfiguration struct {
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
	Kubeletconfig kubeletConfiguration `json:"kubeletconfig"`
}

type ManagerCtx struct {
	PodReadOnlyPort   uint32
	PodAuthorizedPort uint32
	PodClientCertPath string
	DockerAPIVersion  string

	// this is used internally.
	podClientCertPath string
	podClientCertKey  string
}

func kubeletPodListReadOnlyURL(port uint32) string {
	return fmt.Sprintf("http://127.0.0.1:%d/pods", port)
}

func kubeletPodListAuthorizedURL(port uint32) string {
	return fmt.Sprintf("https://127.0.0.1:%d/pods", port)
}

func kubeletConfigAuthorizedURL(port uint32) string {
	return fmt.Sprintf("https://127.0.0.1:%d/configz", port)
}

func kubeletPodListHttpRequest(requestCtx context.Context, ctx *ManagerCtx) (*http.Client, error) {
	client := &http.Client{
		Timeout: kubeletReqTimeout,
	}

	_, err := kubeletFetchPodList(requestCtx, client, kubeletPodListReadOnlyURL(ctx.PodReadOnlyPort))
	return client, err
}

func kubeletPodListAuthorizationRequest(requestCtx context.Context, ctx *ManagerCtx) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(ctx.podClientCertPath, ctx.podClientCertKey)
	if err != nil {
		return nil, fmt.Errorf("loading client key pair [%s,%s]: %w",
			ctx.podClientCertPath, ctx.podClientCertKey, err)
	}

	client := &http.Client{
		Timeout: kubeletReqTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates:       []tls.Certificate{cert},
				InsecureSkipVerify: true, // #nosec G402
			},
		},
	}

	_, err = kubeletFetchPodList(requestCtx, client, kubeletPodListAuthorizedURL(ctx.PodAuthorizedPort))
	return client, err
}

func kubeletPodListPortCacheUpdate(requestCtx context.Context, ctx *ManagerCtx) error {
	if client, err := kubeletPodListHttpRequest(requestCtx, ctx); err == nil {
		kubeletPodListURL = kubeletPodListReadOnlyURL(ctx.PodReadOnlyPort)
		kubeletPodListClient = client
		kubeletPodListRunningEnabled = true
		return nil
	}

	// try to fallback https.
	client, err := kubeletPodListAuthorizationRequest(requestCtx, ctx)
	if err != nil {
		return fmt.Errorf("podlist https: %w", err)
	}

	// update https instance cache
	kubeletPodListClient = client
	kubeletPodListURL = kubeletPodListAuthorizedURL(ctx.PodAuthorizedPort)
	kubeletPodListRunningEnabled = true
	return nil
}

func kubeletGetPodList(ctx context.Context) (corev1.PodList, error) {
	if !kubeletPodListRunningEnabled {
		return corev1.PodList{}, fmt.Errorf("kubelet not running")
	}

	return kubeletFetchPodList(ctx, kubeletPodListClient, kubeletPodListURL)
}

func kubeletPodListDoRequest(client *http.Client, kubeletPodListURL string) (corev1.PodList, error) {
	return kubeletFetchPodList(context.Background(), client, kubeletPodListURL)
}

func kubeletFetchPodList(ctx context.Context, client *http.Client, url string) (corev1.PodList, error) {
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

func kubeletConfigDoRequest(
	client *http.Client,
	kubeletConfigURL string,
) (kubeletConfiguration, error) {
	empty := kubeletConfiguration{}

	body, err := httpDoRequest(client, kubeletConfigURL)
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

func httpDoRequest(client *http.Client, url string) ([]byte, error) {
	return httpRequestContext(context.Background(), client, url)
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

func kubeletContainer(containerID string, container *corev1.Container, containerStatus *corev1.ContainerStatus, pod *corev1.Pod) (*Container, error) {
	// container type
	containerType, err := parseContainerType(container, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type: %w", err)
	}

	// container qos
	containerQos, err := parseContainerQos(containerType, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse qos: %w", err)
	}

	hostname, err := parseContainerHostname(containerType, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse hostname: %w", err)
	}

	// fetch InitPid
	initPid, err := containerInitPid(containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get InitPid: %w", err)
	}

	// net namespace
	nsInum, err := netutil.NetNamespaceInumByPID(initPid)
	if err != nil {
		return nil, fmt.Errorf("failed to get net namespace inum by pid: %w", err)
	}

	// net namespace cookie (Linux 5.14+; falls back to 0 on older kernels)
	netNamespaceCookie, err := netutil.NetNamespaceCookieByPID(initPid)
	if err != nil {
		log.Debugf("failed to get net namespace cookie for pid %d: %v", initPid, err)
	}

	labels, err := parseContainerLabels(containerType, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container labels: %w", err)
	}

	startedAt, err := time.Parse(time.RFC3339, containerStatus.State.Running.StartedAt.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("failed to parse StartedAt %s: %w", containerStatus.State.Running.StartedAt, err)
	}

	css, err := parseContainerCSS(containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container css: %w", err)
	}

	cgroupPath, err := containerCgroupSuffix(containerID, pod)
	if err != nil {
		return nil, fmt.Errorf("failed to get cgroup path: %w", err)
	}

	result := &Container{
		ID:                 containerID,
		Name:               container.Name,
		Hostname:           hostname,
		Type:               containerType,
		Qos:                containerQos,
		IPAddress:          parseContainerIPAddress(pod),
		NetNamespaceInum:   nsInum,
		NetNamespaceCookie: netNamespaceCookie,
		InitPid:            initPid,
		CgroupPath:         cgroupPath,
		CgroupCss:          css,
		StartedAt:          startedAt,
		SyncedAt:           time.Now(),
		lifeResources:      make(map[string]any),
		Labels:             labels,
	}

	// create container life resources
	createContainerLifeResources(result)

	return result, nil
}

func parseContainerIDInPodStatus(data string) (string, error) {
	// containerID example:
	//
	// "containerID": "docker://06ae8891e7e9b80f353e07116980f93a357fb3f239c09894de73b2e74121c94f",
	// "containerID": "containerd://0ac95a0f051b5094551a02b584414773dc24f5b2f1e4ea768460a787f762e279"
	parts := strings.Split(strings.Trim(data, "\""), "://")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid container id: %s", data)
	}

	provider, err := containerProviderFrom(parts[0])
	if err != nil {
		return "", err
	}

	if len(parts[1]) != 64 {
		return "", fmt.Errorf("container id must contain 64 hexadecimal characters: %q", parts[1])
	}
	if err := ValidateContainerID(parts[1]); err != nil {
		return "", err
	}
	if err := initContainerProviderEnv(provider, dockerAPIVersion); err != nil {
		return "", fmt.Errorf("init container provider for containerID %q: %w", data, err)
	}

	return parts[1], nil
}

func parseContainerIPAddress(pod *corev1.Pod) string {
	return pod.Status.PodIP
}

func kubeletConfigFileDefault() (kubeletConfiguration, error) {
	empty := kubeletConfiguration{}

	for _, name := range kubeletDefaultConfigPath {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}

		config := kubeletConfiguration{}
		if err := yaml.Unmarshal(data, &config); err != nil {
			continue
		}

		return config, nil
	}

	return empty, fmt.Errorf("not found kubelet config")
}

// kubeletConfigCacheMustUpdate updates the kubelet configuration cache.
//
// This function MUST succeed: if the kubelet configz endpoint and all
// default config file paths are unavailable, it panics because downstream
// services that depend on kubelet pod information would be broken.
//
// Updated cache vars:
//   - CgroupDriver
//   - ContainerRuntimeEndpoint
func kubeletConfigCacheMustUpdate(ctx *ManagerCtx) error {
	var (
		config kubeletConfiguration
		err    error
	)

	defer func() {
		if config.CgroupDriver != "" {
			kubeletPodCgroupDriver = config.CgroupDriver
		}
		if config.ContainerRuntimeEndpoint != "" {
			kubeletRuntimeEndpoint = config.ContainerRuntimeEndpoint
		}

		log.Debugf("kubelet config cache updated, cgroup driver: %s, runtime: %s",
			kubeletPodCgroupDriver, kubeletRuntimeEndpoint)
	}()

	config, err = kubeletConfigDoRequest(
		kubeletPodListClient,
		kubeletConfigAuthorizedURL(ctx.PodAuthorizedPort),
	)
	if err == nil {
		return nil
	}

	log.Debugf("kubelet config port is not available, try to read config files: %v", kubeletDefaultConfigPath)

	config, err = kubeletConfigFileDefault()
	if err != nil {
		panic(fmt.Sprintf(
			"we cannot find any cgroup driver of kubelet after requesting configz and default files: %v",
			err,
		))
	}

	return nil
}
