// Copyright 2025 The HuaTuo Authors
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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/utils/procfsutil"

	corev1 "k8s.io/api/core/v1"
	kubeletconfig "k8s.io/kubelet/config/v1beta1"
	"sigs.k8s.io/yaml"
)

const (
	kubeletReqTimeout      = 5 * time.Second
	kubeletDefaultConfPath = "/var/lib/kubelet/config.yaml"
)

var (
	kubeletPodListRunningEnabled = false
	kubeletPodListURL            string
	kubeletPodListClient         *http.Client
	kubeletTimeTicker            *time.Ticker
	kubeletDoneCancel            context.CancelFunc
	kubeletPodCgroupDriver       = "cgroupfs"
)

type PodContainerInitCtx struct {
	PodListReadOnlyPort   string
	PodListAuthorizedPort string
	PodClientCertPath     string
	podClientCertPath     string
	podClientCertKey      string
}

func kubeletHttpRequest(ctx *PodContainerInitCtx) (*http.Client, error) {
	client := &http.Client{
		Timeout: kubeletReqTimeout,
	}

	_, err := kubeletDoRequest(client, ctx.PodListReadOnlyPort)
	return client, err
}

func kubeletHttpAuthorizationRequest(ctx *PodContainerInitCtx) (*http.Client, error) {
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

	_, err = kubeletDoRequest(client, ctx.PodListAuthorizedPort)
	return client, err
}

func kubeletPodListPortUpdate(ctx *PodContainerInitCtx) error {
	if client, err := kubeletHttpRequest(ctx); err == nil {
		kubeletPodListURL = ctx.PodListReadOnlyPort
		kubeletPodListClient = client
		kubeletPodListRunningEnabled = true
		return nil
	}

	client, err := kubeletHttpAuthorizationRequest(ctx)
	if err != nil {
		return fmt.Errorf("podlist https: %w", err)
	}

	// update https instance cache
	kubeletPodListClient = client
	kubeletPodListURL = ctx.PodListAuthorizedPort
	kubeletPodListRunningEnabled = true
	return nil
}

func ContainerPodMgrInit(ctx *PodContainerInitCtx) error {
	if ctx.PodListReadOnlyPort == "" && ctx.PodListAuthorizedPort == "" {
		log.Warnf("pod sync is not working, we manually turned off this.")
		return nil
	}

	s := strings.Split(ctx.PodClientCertPath, ",")
	if len(s) == 1 {
		ctx.podClientCertPath, ctx.podClientCertKey = s[0], s[0]
	} else if len(s) >= 2 {
		ctx.podClientCertPath, ctx.podClientCertKey = s[0], s[1]
	}

	_ = kubeletCgroupDriverUpdate()

	err := kubeletPodListPortUpdate(ctx)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		// success or other error codes except connect refused
		// only init css metadata collect when kubelet available.
		if err == nil {
			return containerCgroupCssInit()
		}

		return err
	}

	// syscall.ECONNREFUSED:
	// I hope k8s will be available in the future. :)
	doneCtx, cancel := context.WithCancel(context.Background())

	kubeletDoneCancel = cancel
	kubeletTimeTicker = time.NewTicker(30 * time.Minute)
	go func(doneCtx context.Context, t *time.Ticker) {
		for {
			select {
			case <-t.C:
				if err := kubeletPodListPortUpdate(ctx); err == nil {
					log.Infof("kubelet is running now")
					_ = kubeletCgroupDriverUpdate()
					_ = containerCgroupCssInit()
					ContainerPodMgrClose()
					break
				}
			case <-doneCtx.Done():
				return
			}
		}
	}(doneCtx, kubeletTimeTicker)

	return nil
}

func ContainerPodMgrClose() {
	if kubeletTimeTicker != nil {
		kubeletTimeTicker.Stop()
		kubeletTimeTicker = nil
	}

	if kubeletDoneCancel != nil {
		kubeletDoneCancel()
		kubeletDoneCancel = nil
	}
}

func kubeletSyncContainers() error {
	podList, err := kubeletGetPodList()
	if err != nil {
		// ignore all errors and remain old containers.
		return nil
	}

	type containerInfo struct {
		container       *corev1.Container
		containerStatus *corev1.ContainerStatus
		pod             *corev1.Pod
	}

	// map: ContainerID -> *containerInfo
	newContainers := make(map[string]*containerInfo)
	for i := range podList.Items {
		pod := &podList.Items[i]

		if !isRuningPod(pod) {
			continue
		}

		// map: name -> [*corev1.Container, *corev1.ContainerStatus]
		m := make(map[string][2]any)
		for i := range pod.Spec.Containers {
			container := &pod.Spec.Containers[i]
			m[container.Name] = [2]any{container, nil}
		}
		for i := range pod.Status.ContainerStatuses {
			containerStatus := &pod.Status.ContainerStatuses[i]
			if c, ok := m[containerStatus.Name]; ok {
				m[containerStatus.Name] = [2]any{c[0], containerStatus}
			}
		}

		for _, c := range m {
			containerStatus := c[1].(*corev1.ContainerStatus)
			containerID, err := parseContainerIDInPodStatus(containerStatus.ContainerID)
			if err != nil {
				return fmt.Errorf("failed to parse container id %s in pod %s status: %w", containerStatus.ContainerID, pod.Name, err)
			}

			newContainers[containerID] = &containerInfo{
				container:       c[0].(*corev1.Container),
				containerStatus: containerStatus,
				pod:             pod,
			}
		}
	}

	for k := range containers {
		// clear old containers which do not exist in newContainers.
		if _, ok := newContainers[k]; !ok {
			delete(containers, k)
			continue
		}

		// skip the existing containers
		delete(newContainers, k)
	}

	// update containers.
	for newContainerID, newContainerInfo := range newContainers {
		container := newContainerInfo.container
		containerStatus := newContainerInfo.containerStatus
		pod := newContainerInfo.pod

		if err := kubeletUpdateContainer(newContainerID, container, containerStatus, pod); err != nil {
			log.Infof("failed to update container %s in pod %s: %v", newContainerID, pod.Name, err)
			continue
		}
	}

	return nil
}

func kubeletGetPodList() (corev1.PodList, error) {
	if !kubeletPodListRunningEnabled {
		return corev1.PodList{}, fmt.Errorf("kubelet not running")
	}

	return kubeletDoRequest(kubeletPodListClient, kubeletPodListURL)
}

func kubeletDoRequest(client *http.Client, kubeletPodListURL string) (corev1.PodList, error) {
	podList := corev1.PodList{}
	req, err := http.NewRequest(http.MethodGet, kubeletPodListURL, http.NoBody)
	if err != nil {
		return podList, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return podList, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return podList, fmt.Errorf("http: %s, status: %d, body: %s", kubeletPodListURL, resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, &podList); err != nil {
		return podList, fmt.Errorf("http: %s, Unmarshal: %w, body: %s", kubeletPodListURL, err, string(body))
	}

	return podList, nil
}

// func updateKubeletContainer(containerID string, container *corev1.Container, containerStatus *corev1.ContainerStatus, pod *corev1.Pod, css map[string]uint64) error {
func kubeletUpdateContainer(containerID string, container *corev1.Container, containerStatus *corev1.ContainerStatus, pod *corev1.Pod) error {
	// container type
	containerType, err := parseContainerType(container, pod)
	if err != nil {
		return fmt.Errorf("failed to parse type: %w", err)
	}

	// container qos
	containerQos, err := parseContainerQos(containerType, pod)
	if err != nil {
		return fmt.Errorf("failed to parse qos: %w", err)
	}

	hostname, err := parseContainerHostname(containerType, pod)
	if err != nil {
		return fmt.Errorf("failed to parse hostname: %w", err)
	}

	// fetch InitPid
	initPid, err := containerInitPid(containerID)
	if err != nil {
		return fmt.Errorf("failed to get InitPid: %w", err)
	}

	// net namespace
	nsInode, err := procfsutil.NetNSInodeByPid(initPid)
	if err != nil {
		return fmt.Errorf("failed to get net namespace inode by pid: %w", err)
	}

	labels, err := parseContainerLabels(containerType, pod)
	if err != nil {
		return fmt.Errorf("failed to parse container labels: %w", err)
	}

	startedAt, err := time.Parse(time.RFC3339, containerStatus.State.Running.StartedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("failed to parse StartedAt %s: %w", containerStatus.State.Running.StartedAt, err)
	}

	css, err := parseContainerCSS(containerID)
	if err != nil {
		return fmt.Errorf("failed to parse container css: %w", err)
	}

	containers[containerID] = &Container{
		ID:                containerID,
		Name:              container.Name,
		Hostname:          hostname,
		Type:              containerType,
		Qos:               containerQos,
		IPAddress:         parseContainerIPAddress(pod),
		NetNamespaceInode: nsInode,
		InitPid:           initPid,
		CgroupSuffix:      containerCgroupSuffix(containerID, pod),
		CSS:               css,
		StartedAt:         startedAt,
		SyncedAt:          time.Now(),
		lifeResouces:      make(map[string]any),
		Labels:            labels,
	}

	// create container life resources
	createContainerLifeResources(containers[containerID])

	log.Infof("update container %#v", containers[containerID])
	return nil
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

	// init the container provider
	initContainerProviderEnv(parts[0])

	return parts[1], nil
}

func parseContainerIPAddress(pod *corev1.Pod) string {
	return pod.Status.PodIP
}

func isRuningPod(pod *corev1.Pod) bool {
	// The Pod has been bound to a node, and all of the containers have been created.
	// At least one container is still running, or is in the process of starting or
	// restarting.
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	// all containers are running.
	for i := range pod.Status.ContainerStatuses {
		containerStatus := &pod.Status.ContainerStatuses[i]
		if containerStatus.State.Running == nil {
			return false
		}
	}

	return true
}

func kubeletCgroupDriverUpdate() error {
	data, err := os.ReadFile(kubeletDefaultConfPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", kubeletDefaultConfPath, err)
	}

	var config kubeletconfig.KubeletConfiguration

	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	// cgroupfs as default of kubelet
	// config.CgroupDriver is read from config file, which may be any
	// string, such as systemdxxx (in this case, kubelet use cgroupfs)
	if config.CgroupDriver == "systemd" {
		kubeletPodCgroupDriver = config.CgroupDriver
	}

	return nil
}
