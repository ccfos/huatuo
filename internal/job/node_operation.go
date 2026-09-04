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
	"fmt"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
)

func (m *Manager) startOperation(ctx context.Context, job *Job) (*nodeapi.Operation, error) {
	request, err := startOperationRequest(job)
	if err != nil {
		return nil, err
	}
	return m.nodeClient.StartOperation(ctx, job.Hostname, request)
}

func startOperationRequest(job *Job) (*nodeapi.StartOperationRequest, error) {
	request := &nodeapi.StartOperationRequest{
		RequestID:       job.ID,
		DurationSeconds: int64(job.Duration / time.Second),
		Scope:           apiv1.ObservationScope(job.Scope),
	}
	if job.ContainerID != "" {
		request.ContainerID = &job.ContainerID
	}
	switch job.Kind {
	case KindProfiling:
		spec := job.Spec.Profiling
		profilingSpec := nodeapi.ProfilingOperationSpec{
			Type:     nodeapi.ProfilingType(spec.Type),
			Language: nodeapi.ProfilingLanguage(spec.Language),
			Mode:     nodeapi.ProfilingMode(spec.Mode),
		}
		if spec.BinaryMatchPath != "" {
			profilingSpec.BinaryMatchPath = &spec.BinaryMatchPath
		}
		request.Kind = nodeapi.OperationKindProfiling
		return request, request.Spec.FromProfilingOperationSpec(profilingSpec)
	case KindTracing:
		request.Kind = nodeapi.OperationKindTracing
		return request, request.Spec.FromTracingOperationSpec(nodeapi.TracingOperationSpec{
			Type: nodeapi.TracingType(job.Spec.Tracing.Type),
		})
	default:
		return nil, fmt.Errorf("%w: unsupported Job kind %q", ErrUnsupportedKind, job.Kind)
	}
}
