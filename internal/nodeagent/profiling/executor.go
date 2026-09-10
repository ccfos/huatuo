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

package profiling

import (
	"context"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/exec"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

type executor struct {
	process   *exec.Process
	stream    *toolstream.Server
	publisher ResultPublisher
	requestID string
}

func newExecutor(
	process *exec.Process,
	stream *toolstream.Server,
	publisher ResultPublisher,
	requestID string,
) *executor {
	return &executor{
		process:   process,
		stream:    stream,
		publisher: publisher,
		requestID: requestID,
	}
}

func (e *executor) Start(ctx context.Context) error {
	if err := e.stream.ExpectSession(types.ProfilingToolName, e.requestID); err != nil {
		return fmt.Errorf("expect profiler result stream: %w", err)
	}
	if err := e.process.Start(ctx); err != nil {
		e.stream.CancelSession(types.ProfilingToolName, e.requestID)
		return e.withOutput("start profiler", err)
	}
	return nil
}

func (e *executor) Wait() error {
	err := e.process.Wait()
	if errors.Is(err, exec.ErrStopped) {
		return errors.Join(operation.ErrStopped, err)
	}
	if err != nil {
		return e.withOutput("wait for profiler", err)
	}
	return nil
}

func (e *executor) Stop(ctx context.Context) error {
	if err := e.process.Stop(ctx); err != nil {
		return e.withOutput("stop profiler", err)
	}
	return nil
}

func (e *executor) Finalize(ctx context.Context, mode operation.FinalizeMode) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("finalize profiler: %w", err)
	}
	switch mode {
	case operation.FinalizePublish:
		if err := e.stream.AwaitSession(ctx, types.ProfilingToolName, e.requestID); err != nil {
			return fmt.Errorf("finalize profiler result stream: %w", err)
		}
		if err := e.publisher.Publish(ctx, e.requestID); err != nil {
			return fmt.Errorf("finalize profiler publication: %w", err)
		}
		return nil
	case operation.FinalizeDiscard:
		e.stream.CancelSession(types.ProfilingToolName, e.requestID)
		return nil
	default:
		return fmt.Errorf("finalize profiler: unsupported mode %d", mode)
	}
}

func (e *executor) withOutput(action string, err error) error {
	output := e.process.Stdout()
	if stderr := e.process.Stderr(); len(stderr) > 0 {
		if len(output) > 0 {
			output = append(output, '\n')
		}
		output = append(output, stderr...)
	}
	if len(output) == 0 {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w; output: %s", action, err, output)
}
