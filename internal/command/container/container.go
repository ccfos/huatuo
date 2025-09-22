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

package container

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"huatuo-bamai/internal/pod"
)

func getContainers(serverAddr, containerID string) ([]pod.Container, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/containers/json", serverAddr), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("new request failed: %w", err)
	}

	if containerID != "" {
		req.URL.RawQuery = fmt.Sprintf("container_id=%s", containerID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get container failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get container failed, status code: %d", resp.StatusCode)
	}

	var containers []pod.Container
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("unmarshal resp.body failed: %w", err)
	}

	return containers, nil
}

// GetContainerByID get container by container id
func GetContainerByID(serverAddr, containerID string) (*pod.Container, error) {
	containers, err := getContainers(serverAddr, containerID)
	if err != nil {
		return nil, err
	}

	if len(containers) == 0 {
		return nil, fmt.Errorf("container not found: %s", containerID)
	}

	return &containers[0], nil
}

// GetAllContainers get all containers
func GetAllContainers(serverAddr string) ([]pod.Container, error) {
	return getContainers(serverAddr, "")
}
