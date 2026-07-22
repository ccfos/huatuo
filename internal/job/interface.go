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
	Get(ctx context.Context, jobID string) (*Job, error)
	Create(ctx context.Context, job *Job) error
	Save(ctx context.Context, job *Job) error
	Delete(ctx context.Context, jobID string) error
	List(ctx context.Context, query *JobQuery) ([]*Job, error)
	Count(ctx context.Context, query *JobQuery) (int64, error)
	Close(ctx context.Context) error
}

// NodeAgent interface for communicating with the huatuo-bamai agent
type NodeAgent interface {
	StartTaskContext(ctx context.Context, host, container string, request *AgentTaskRequest) (string, error)
	StopTaskContext(ctx context.Context, host, taskID string, force bool) error
	GetTaskStatusContext(ctx context.Context, host, taskID string) (string, *Result, error)
}
