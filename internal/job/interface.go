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
	"context"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
)

// Store persists Job snapshots and atomic state transitions.
type Store interface {
	Get(ctx context.Context, jobID string) (*Job, error)
	Create(ctx context.Context, job *Job) error
	Save(ctx context.Context, job *Job, expectedStatuses ...Status) error
	List(ctx context.Context, query *Query) ([]*Job, error)
	Count(ctx context.Context, query *Query) (int64, error)
	DeleteTerminalBefore(ctx context.Context, endedBefore time.Time, limit int) (int64, error)
	Close(ctx context.Context) error
}

// NodeClient controls typed Node Operations without owning Job state.
type NodeClient interface {
	StartProfiling(
		ctx context.Context,
		host string,
		request *nodeapi.StartProfilingRequest,
	) (*nodeapi.Operation, error)
	GetProfiling(ctx context.Context, host, requestID string) (*nodeapi.Operation, error)
	StopProfiling(ctx context.Context, host, requestID string) (*nodeapi.Operation, error)
	StartTracing(
		ctx context.Context,
		host string,
		request *nodeapi.StartTracingRequest,
	) (*nodeapi.Operation, error)
	GetTracing(ctx context.Context, host, requestID string) (*nodeapi.Operation, error)
	StopTracing(ctx context.Context, host, requestID string) (*nodeapi.Operation, error)
}
