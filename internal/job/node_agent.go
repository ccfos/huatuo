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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"huatuo-bamai/internal/log"
)

// HTTPNodeAgent implements NodeAgent interface using HTTP
type HTTPNodeAgent struct {
	client              *http.Client
	port                int
	statusRetryAttempts int
	statusRetryBackoff  time.Duration
	observe             AgentRequestObserver
}

// AgentRequestObserver records one completed Agent request.
type AgentRequestObserver func(operation string, duration time.Duration, err error)

const (
	maxAgentResponseBytes = 1 << 20
	maxAgentErrorBytes    = 8 << 10
)

type HTTPNodeAgentConfig struct {
	Client              *http.Client
	Port                int
	RequestTimeout      time.Duration
	StatusRetryAttempts int
	StatusRetryBackoff  time.Duration
	Observe             AgentRequestObserver
}

type startTaskRequest struct {
	RequestID         string   `json:"request_id,omitempty"`
	TracerName        string   `json:"tracer_name"`
	Timeout           int      `json:"timeout"`
	Interval          int      `json:"interval,omitempty"`
	Duration          int      `json:"duration,omitempty"`
	DataType          string   `json:"data_type"`
	ContainerID       string   `json:"container_id,omitempty"`
	ContainerHostname string   `json:"container_hostname,omitempty"`
	TracerArgs        []string `json:"trace_args,omitempty"`
}

// NewHTTPNodeAgent creates a new HTTP agent client
func NewHTTPNodeAgent(configs ...HTTPNodeAgentConfig) *HTTPNodeAgent {
	config := HTTPNodeAgentConfig{
		Port:                19704,
		RequestTimeout:      10 * time.Second,
		StatusRetryAttempts: 3,
		StatusRetryBackoff:  100 * time.Millisecond,
	}
	if len(configs) > 0 {
		config = configs[0]
		if config.Port == 0 {
			config.Port = 19704
		}
		if config.RequestTimeout <= 0 {
			config.RequestTimeout = 10 * time.Second
		}
		if config.StatusRetryAttempts <= 0 {
			config.StatusRetryAttempts = 3
		}
		if config.StatusRetryBackoff <= 0 {
			config.StatusRetryBackoff = 100 * time.Millisecond
		}
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: config.RequestTimeout}
	}
	return &HTTPNodeAgent{
		client:              config.Client,
		port:                config.Port,
		statusRetryAttempts: config.StatusRetryAttempts,
		statusRetryBackoff:  config.StatusRetryBackoff,
		observe:             config.Observe,
	}
}

// StartTask starts a task on the agent
func (c *HTTPNodeAgent) StartTask(host, container string, args *AgentTaskRequest) (string, error) {
	return c.StartTaskContext(context.Background(), host, container, args)
}

func (c *HTTPNodeAgent) StartTaskContext(
	ctx context.Context,
	host, container string,
	args *AgentTaskRequest,
) (taskID string, returnedErr error) {
	startedAt := time.Now()
	defer func() { c.observeRequest("start", startedAt, returnedErr) }()
	taskArgs := *args
	taskArgs.ContainerID = container
	requestBodyBytes, err := json.Marshal(startTaskRequest{
		RequestID:         taskArgs.RequestID,
		TracerName:        taskArgs.TracerName,
		Timeout:           taskArgs.TraceTimeout,
		Interval:          taskArgs.Interval,
		Duration:          taskArgs.Duration,
		DataType:          taskArgs.DataType,
		ContainerID:       taskArgs.ContainerID,
		ContainerHostname: taskArgs.ContainerHostname,
		TracerArgs:        taskArgs.TracerArgs,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	endpoint := c.endpoint(host, "/tasks")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrAgentDispatchUncertain, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := readAgentErrorBody(resp.Body)
		if err != nil {
			return "", fmt.Errorf("agent returned non-OK status: %d, read body: %w", resp.StatusCode, err)
		}
		return "", fmt.Errorf("agent returned non-OK status: %d, body: %s", resp.StatusCode, body)
	}

	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	body, err := readAgentBody(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	if response.Code != 0 {
		return "", fmt.Errorf("agent returned error response: code=%d, message=%s", response.Code, response.Message)
	}
	if response.Data.TaskID == "" {
		return "", fmt.Errorf("agent returned empty task_id")
	}

	return response.Data.TaskID, nil
}

// StopTask stops a task on the agent
func (c *HTTPNodeAgent) StopTask(host, taskID string, force bool) error {
	return c.StopTaskContext(context.Background(), host, taskID, force)
}

func (c *HTTPNodeAgent) StopTaskContext(
	ctx context.Context,
	host, taskID string,
	force bool,
) (returnedErr error) {
	startedAt := time.Now()
	defer func() { c.observeRequest("stop", startedAt, returnedErr) }()
	endpoint := c.endpoint(host, "/tasks/"+url.PathEscape(taskID))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusNoContent {
		body, err := readAgentErrorBody(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to stop job: %s, read body: %w", resp.Status, err)
		}
		return fmt.Errorf("failed to stop job: %s, body: %s", resp.Status, string(body))
	}

	return nil
}

// GetTaskStatus gets the status of a task on the agent
func (c *HTTPNodeAgent) GetTaskStatus(host, taskID string) (string, *Result, error) {
	return c.GetTaskStatusContext(context.Background(), host, taskID)
}

func (c *HTTPNodeAgent) GetTaskStatusContext(
	ctx context.Context,
	host, taskID string,
) (status string, result *Result, returnedErr error) {
	startedAt := time.Now()
	defer func() { c.observeRequest("status", startedAt, returnedErr) }()
	endpoint := c.endpoint(host, "/tasks/"+url.PathEscape(taskID))

	var lastErr error
	for attempt := range c.statusRetryAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		if err != nil {
			return "", nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			// Only retry on timeout error
			if nerr, ok := err.(interface{ Timeout() bool }); ok && nerr.Timeout() {
				lastErr = err
				log.WithField("task_id", taskID).WithField("attempt", attempt+1).
					Info("timed out getting task status; retrying")
				select {
				case <-time.After(time.Duration(attempt+1) * c.statusRetryBackoff):
					continue
				case <-ctx.Done():
					return "", nil, ctx.Err()
				}
			}
			return "", nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, err := readAgentErrorBody(resp.Body)
			if err != nil {
				return "", nil, fmt.Errorf("agent returned non-OK status: %d, read body: %w", resp.StatusCode, err)
			}
			return "", nil, fmt.Errorf("agent returned non-OK status: %d, body: %s", resp.StatusCode, string(body))
		}

		var outerResponse struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		body, err := readAgentBody(resp.Body)
		if err != nil {
			return "", nil, fmt.Errorf("failed to read response: %w", err)
		}
		if err := json.Unmarshal(body, &outerResponse); err != nil {
			return "", nil, fmt.Errorf("failed to decode response: %w", err)
		}
		if outerResponse.Code != 0 {
			return "", nil, fmt.Errorf("agent returned error response: code=%d, message=%s", outerResponse.Code, outerResponse.Message)
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
	return "", nil, fmt.Errorf("failed to send request after %d attempts: %w", c.statusRetryAttempts, lastErr)
}

func (c *HTTPNodeAgent) observeRequest(operation string, startedAt time.Time, err error) {
	if c.observe != nil {
		c.observe(operation, time.Since(startedAt), err)
	}
}

func (c *HTTPNodeAgent) endpoint(host, path string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(c.port)) + path
}

func readAgentBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxAgentResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAgentResponseBytes {
		return nil, fmt.Errorf("agent response exceeds %d bytes", maxAgentResponseBytes)
	}
	return data, nil
}

func readAgentErrorBody(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, maxAgentErrorBytes))
}
