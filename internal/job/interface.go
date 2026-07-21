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

import "context"

type Store interface {
	Get(jobID string) (*Job, error)
	Save(job *Job) error
	Delete(jobID string) error
	List(query *JobQuery) ([]*Job, error)
}

type contextStore interface {
	GetContext(ctx context.Context, jobID string) (*Job, error)
	SaveContext(ctx context.Context, job *Job) error
	DeleteContext(ctx context.Context, jobID string) error
	ListContext(ctx context.Context, query *JobQuery) ([]*Job, error)
	Close(ctx context.Context) error
}

// NodeAgent interface for communicating with the huatuo-bamai agent
type NodeAgent interface {
	// StartTask starts a task on the agent
	StartTask(host, container string, request *AgentTaskRequest) (string, error)
	// StopTask stops a task on the agent
	StopTask(host, taskID string, force bool) error
	// GetTaskStatus gets the status of a task on the agent
	GetTaskStatus(host, taskID string) (string, *Result, error)
}

type contextNodeAgent interface {
	StartTaskContext(ctx context.Context, host, container string, request *AgentTaskRequest) (string, error)
	StopTaskContext(ctx context.Context, host, taskID string, force bool) error
	GetTaskStatusContext(ctx context.Context, host, taskID string) (string, *Result, error)
}
