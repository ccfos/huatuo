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

	"huatuo-bamai/internal/nodeagent/command"
	"huatuo-bamai/internal/nodeagent/operation"
)

type executor struct {
	process *command.Process
}

func newExecutor(process *command.Process) *executor {
	return &executor{process: process}
}

func (e *executor) Start(ctx context.Context) error {
	if err := e.process.Start(ctx); err != nil {
		return e.withOutput("start profiler", err)
	}
	return nil
}

func (e *executor) Wait() error {
	err := e.process.Wait()
	if errors.Is(err, command.ErrStopped) {
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

func (*executor) Finalize(ctx context.Context, mode operation.FinalizeMode) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("finalize profiler: %w", err)
	}
	switch mode {
	case operation.FinalizePublish, operation.FinalizeDiscard:
		return nil
	default:
		return fmt.Errorf("finalize profiler: unsupported mode %d", mode)
	}
}

func (e *executor) withOutput(action string, err error) error {
	output := e.process.OutputTail()
	if output == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w; output tail: %s", action, err, output)
}
