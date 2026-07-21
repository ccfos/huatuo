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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type timeoutTransportError struct{}

func (timeoutTransportError) Error() string {
	return "request timeout"
}

func (timeoutTransportError) Timeout() bool {
	return true
}

func newHTTPNodeAgentWithTransport(transport http.RoundTripper) *HTTPNodeAgent {
	agent := NewHTTPNodeAgent()
	agent.client.Transport = transport
	return agent
}

func newHTTPResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

var errNodeAgentBodyRead = errors.New("node agent response body read failed")

type failingReadCloser struct{}

func (failingReadCloser) Read(_ []byte) (int, error) {
	return 0, errNodeAgentBodyRead
}

func (failingReadCloser) Close() error {
	return nil
}

func newHTTPResponseWithBody(statusCode int, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:     make(http.Header),
		Body:       body,
	}
}

func TestHTTPNodeAgentReturnsBodyReadError(t *testing.T) {
	t.Run("start task", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponseWithBody(http.StatusInternalServerError, failingReadCloser{}), nil
		}))

		_, err := agent.StartTask("huatuo-dev", "payment-worker", &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		})
		if !errors.Is(err, errNodeAgentBodyRead) {
			t.Errorf("StartTask() error=%v, want body read error", err)
		}
	})

	t.Run("stop task", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponseWithBody(http.StatusConflict, failingReadCloser{}), nil
		}))

		err := agent.StopTask("huatuo-dev", "agent-task-2026", true)
		if !errors.Is(err, errNodeAgentBodyRead) {
			t.Errorf("StopTask() error=%v, want body read error", err)
		}
	})

	t.Run("get task status", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponseWithBody(http.StatusInternalServerError, failingReadCloser{}), nil
		}))

		_, _, err := agent.GetTaskStatus("huatuo-dev", "agent-task-2026")
		if !errors.Is(err, errNodeAgentBodyRead) {
			t.Errorf("GetTaskStatus() error=%v, want body read error", err)
		}
	})
}

// TestHTTPNodeAgentStartTask tests HTTPNodeAgent.StartTask request and response handling, including successful task dispatch, writing container name to request body, agent returning non-200, and error handling for unparseable response body.
func TestHTTPNodeAgentStartTask(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var requestBody string
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Errorf("request method=%s, want %s", req.Method, http.MethodPost)
			}
			if req.URL.String() != "http://huatuo-dev:19704/tasks" {
				t.Errorf("request url=%q, want %q", req.URL.String(), "http://huatuo-dev:19704/tasks")
			}
			if req.Header.Get("Content-Type") != "application/json" {
				t.Errorf("request content type=%q, want %q", req.Header.Get("Content-Type"), "application/json")
			}

			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("ReadAll(req.Body) error=%v, want nil", err)
			}
			requestBody = string(bodyBytes)

			return newHTTPResponse(http.StatusOK, `{"code":0,"message":"ok","data":{"task_id":"agent-task-2026"}}`), nil
		}))
		args := &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			Interval:     10,
			Duration:     120,
			DataType:     "flamegraph",
			TracerArgs:   []string{"--duration", "60", "--aggr-interval", "10"},
		}

		taskID, err := agent.StartTask("huatuo-dev", "payment-worker", args)
		if err != nil {
			t.Errorf("StartTask() error=%v, want nil", err)
		}
		if taskID != "agent-task-2026" {
			t.Errorf("StartTask() taskID=%q, want %q", taskID, "agent-task-2026")
		}
		if args.ContainerID != "payment-worker" {
			t.Errorf("StartTask() ContainerID=%q, want %q", args.ContainerID, "payment-worker")
		}
		if !strings.Contains(requestBody, `"container_id":"payment-worker"`) {
			t.Errorf("StartTask() request body=%q, want container ID field", requestBody)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(requestBody), &payload); err != nil {
			t.Fatalf("json.Unmarshal(requestBody) error=%v", err)
		}
		if got := payload["timeout"]; got != float64(60) {
			t.Errorf("StartTask() timeout payload=%v, want 60", got)
		}
		if _, ok := payload["trace_timeout"]; ok {
			t.Errorf("StartTask() request body=%q, should use agent field timeout instead of trace_timeout", requestBody)
		}
		if got := payload["interval"]; got != float64(10) {
			t.Errorf("StartTask() interval payload=%v, want 10", got)
		}
		if got := payload["duration"]; got != float64(120) {
			t.Errorf("StartTask() duration payload=%v, want 120", got)
		}
		if got := payload["trace_args"]; fmt.Sprint(got) != "[--duration 60 --aggr-interval 10]" {
			t.Errorf("StartTask() trace_args payload=%v, want profiler duration and interval", got)
		}
	})

	t.Run("non ok response", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponse(http.StatusInternalServerError, "agent unavailable"), nil
		}))

		taskID, err := agent.StartTask("huatuo-dev", "payment-worker", &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		})
		if err == nil || !strings.Contains(err.Error(), "agent returned non-OK status") {
			t.Errorf("StartTask() error=%v, want non-OK status error", err)
		}
		if taskID != "" {
			t.Errorf("StartTask() taskID=%q, want empty", taskID)
		}
	})

	t.Run("invalid response body", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponse(http.StatusOK, `{"code":0,"message":"ok","data":`), nil
		}))

		taskID, err := agent.StartTask("huatuo-dev", "payment-worker", &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		})
		if err == nil || !strings.Contains(err.Error(), "failed to decode response") {
			t.Errorf("StartTask() error=%v, want decode error", err)
		}
		if taskID != "" {
			t.Errorf("StartTask() taskID=%q, want empty", taskID)
		}
	})

	t.Run("agent error response", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponse(http.StatusOK, `{"code":400,"message":"missing timeout","data":null}`), nil
		}))

		taskID, err := agent.StartTask("huatuo-dev", "payment-worker", &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		})
		if err == nil || !strings.Contains(err.Error(), "agent returned error response") {
			t.Errorf("StartTask() error=%v, want agent error response", err)
		}
		if taskID != "" {
			t.Errorf("StartTask() taskID=%q, want empty", taskID)
		}
	})

	t.Run("empty task id", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponse(http.StatusOK, `{"code":0,"message":"ok","data":{"task_id":""}}`), nil
		}))

		taskID, err := agent.StartTask("huatuo-dev", "payment-worker", &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		})
		if err == nil || !strings.Contains(err.Error(), "empty task_id") {
			t.Errorf("StartTask() error=%v, want empty task_id error", err)
		}
		if taskID != "" {
			t.Errorf("StartTask() taskID=%q, want empty", taskID)
		}
	})
}

// TestHTTPNodeAgentStopTask tests HTTPNodeAgent.StopTask DELETE request behavior, including successful 204 response, and including status code and response body in error message when agent returns non-204.
func TestHTTPNodeAgentStopTask(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodDelete {
				t.Errorf("request method=%s, want %s", req.Method, http.MethodDelete)
			}
			if req.URL.String() != "http://huatuo-dev:19704/tasks/agent-task-2026" {
				t.Errorf("request url=%q, want %q", req.URL.String(), "http://huatuo-dev:19704/tasks/agent-task-2026")
			}
			return newHTTPResponse(http.StatusNoContent, ""), nil
		}))

		err := agent.StopTask("huatuo-dev", "agent-task-2026", true)
		if err != nil {
			t.Errorf("StopTask() error=%v, want nil", err)
		}
	})

	t.Run("non no content response", func(t *testing.T) {
		agent := newHTTPNodeAgentWithTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newHTTPResponse(http.StatusConflict, "task still running"), nil
		}))

		err := agent.StopTask("huatuo-dev", "agent-task-2026", true)
		if err == nil || !strings.Contains(err.Error(), "failed to stop job") {
			t.Errorf("StopTask() error=%v, want stop failure", err)
		}
	})
}

// TestHTTPNodeAgentGetTaskStatus tests HTTPNodeAgent.GetTaskStatus status query logic, including successful nested response parsing, retry success after timeout, failure after 3 consecutive timeouts, and immediate return without retry on non-timeout errors.
func TestHTTPNodeAgentGetTaskStatus(t *testing.T) {
	cases := []struct {
		name           string
		buildTransport func(attempts *int) http.RoundTripper
		validate       func(t *testing.T, status string, result *Result, err error, attempts int)
	}{
		{
			name: "success",
			buildTransport: func(attempts *int) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					*attempts++
					if req.Method != http.MethodGet {
						t.Errorf("request method=%s, want %s", req.Method, http.MethodGet)
					}
					if req.URL.String() != "http://huatuo-dev:19704/tasks/agent-task-2026" {
						t.Errorf("request url=%q, want %q", req.URL.String(), "http://huatuo-dev:19704/tasks/agent-task-2026")
					}
					return newHTTPResponse(http.StatusOK, `{"code":0,"message":"ok","data":{"status":"completed","data":"s3://huatuo-region/job-report-2026","error":""}}`), nil
				})
			},
			validate: func(t *testing.T, status string, result *Result, err error, attempts int) {
				if err != nil {
					t.Errorf("GetTaskStatus() error=%v, want nil", err)
				}
				if status != AgentStatusCompleted {
					t.Errorf("GetTaskStatus() status=%q, want %q", status, AgentStatusCompleted)
				}
				if result == nil {
					t.Errorf("GetTaskStatus() result=nil, want non-nil")
					return
				}
				if result.URL != "s3://huatuo-region/job-report-2026" {
					t.Errorf("GetTaskStatus() result.URL=%q, want %q", result.URL, "s3://huatuo-region/job-report-2026")
				}
				if attempts != 1 {
					t.Errorf("GetTaskStatus() attempts=%d, want 1", attempts)
				}
			},
		},
		{
			name: "timeout retry then success",
			buildTransport: func(attempts *int) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					*attempts++
					if *attempts < 3 {
						return nil, timeoutTransportError{}
					}
					return newHTTPResponse(http.StatusOK, `{"code":0,"message":"ok","data":{"status":"running","data":"","error":""}}`), nil
				})
			},
			validate: func(t *testing.T, status string, result *Result, err error, attempts int) {
				if err != nil {
					t.Errorf("GetTaskStatus() error=%v, want nil", err)
				}
				if status != AgentStatusRunning {
					t.Errorf("GetTaskStatus() status=%q, want %q", status, AgentStatusRunning)
				}
				if result == nil {
					t.Errorf("GetTaskStatus() result=nil, want non-nil")
				}
				if attempts != 3 {
					t.Errorf("GetTaskStatus() attempts=%d, want 3", attempts)
				}
			},
		},
		{
			name: "timeout retry exhausted",
			buildTransport: func(attempts *int) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					*attempts++
					return nil, timeoutTransportError{}
				})
			},
			validate: func(t *testing.T, status string, result *Result, err error, attempts int) {
				if err == nil || !strings.Contains(err.Error(), "failed to send request after 3 attempts") {
					t.Errorf("GetTaskStatus() error=%v, want retry exhausted error", err)
				}
				if status != "" {
					t.Errorf("GetTaskStatus() status=%q, want empty", status)
				}
				if result != nil {
					t.Errorf("GetTaskStatus() result=%+v, want nil", result)
				}
				if attempts != 3 {
					t.Errorf("GetTaskStatus() attempts=%d, want 3", attempts)
				}
			},
		},
		{
			name: "agent error response",
			buildTransport: func(attempts *int) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					*attempts++
					return newHTTPResponse(http.StatusOK, `{"code":500,"message":"task status failed","data":null}`), nil
				})
			},
			validate: func(t *testing.T, status string, result *Result, err error, attempts int) {
				if err == nil || !strings.Contains(err.Error(), "agent returned error response") {
					t.Errorf("GetTaskStatus() error=%v, want agent error response", err)
				}
				if status != "" {
					t.Errorf("GetTaskStatus() status=%q, want empty", status)
				}
				if result != nil {
					t.Errorf("GetTaskStatus() result=%+v, want nil", result)
				}
				if attempts != 1 {
					t.Errorf("GetTaskStatus() attempts=%d, want 1", attempts)
				}
			},
		},
		{
			name: "non timeout error returns immediately",
			buildTransport: func(attempts *int) http.RoundTripper {
				return roundTripFunc(func(req *http.Request) (*http.Response, error) {
					*attempts++
					return nil, errors.New("connection refused")
				})
			},
			validate: func(t *testing.T, status string, result *Result, err error, attempts int) {
				if err == nil || !strings.Contains(err.Error(), "failed to send request") {
					t.Errorf("GetTaskStatus() error=%v, want send request error", err)
				}
				if status != "" {
					t.Errorf("GetTaskStatus() status=%q, want empty", status)
				}
				if result != nil {
					t.Errorf("GetTaskStatus() result=%+v, want nil", result)
				}
				if attempts != 1 {
					t.Errorf("GetTaskStatus() attempts=%d, want 1", attempts)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			agent := newHTTPNodeAgentWithTransport(tc.buildTransport(&attempts))

			status, result, err := agent.GetTaskStatus("huatuo-dev", "agent-task-2026")
			tc.validate(t, status, result, err, attempts)
		})
	}
}
