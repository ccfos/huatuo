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

package job

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"huatuo-bamai/internal/log"
)

// HTTPNodeAgent implements NodeAgent interface using HTTP
type HTTPNodeAgent struct {
	client *http.Client
}

// NewHTTPNodeAgent creates a new HTTP agent client
func NewHTTPNodeAgent() *HTTPNodeAgent {
	return &HTTPNodeAgent{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// StartTask starts a task on the agent
func (c *HTTPNodeAgent) StartTask(host, container string, args *NewAgentTaskReq) (string, error) {
	args.ContainerHostname = container
	requestBodyBytes, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := fmt.Sprintf("http://%s:19704/tasks", host)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("agent returned non-OK status: %d, body: %s", resp.StatusCode, body)
	}

	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data.TaskID, nil
}

// StopTask stops a task on the agent
func (c *HTTPNodeAgent) StopTask(host, taskID string, force bool) error {
	url := fmt.Sprintf("http://%s:19704/tasks/%s", host, taskID)

	req, err := http.NewRequest(http.MethodDelete, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to stop job: %s, body: %s", resp.Status, string(body))
	}

	return nil
}

// GetTaskStatus gets the status of a task on the agent
func (c *HTTPNodeAgent) GetTaskStatus(host, taskID string) (string, *Result, error) {
	url := fmt.Sprintf("http://%s:19704/tasks/%s", host, taskID)

	var lastErr error
	for attempt := range 3 {
		req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
		if err != nil {
			return "", nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			// Only retry on timeout error
			if nerr, ok := err.(interface{ Timeout() bool }); ok && nerr.Timeout() {
				lastErr = err
				log.Infof("timeout to get task status, retry, taskID: %s, attempt: %d", taskID, attempt+1)
				continue
			}
			return "", nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return "", nil, fmt.Errorf("agent returned non-OK status: %d, body: %s", resp.StatusCode, string(body))
		}

		var outerResponse struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&outerResponse); err != nil {
			return "", nil, fmt.Errorf("failed to decode response: %w", err)
		}

		var innerResponse struct {
			Status string `json:"status"`
			Data   string `json:"data"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(outerResponse.Data, &innerResponse); err != nil {
			return "", nil, fmt.Errorf("failed to decode inner response data: %w", err)
		}

		return innerResponse.Status, &Result{URL: innerResponse.Data, Error: innerResponse.Error}, nil
	}
	return "", nil, fmt.Errorf("failed to send request after 3 attempts: %w", lastErr)
}
