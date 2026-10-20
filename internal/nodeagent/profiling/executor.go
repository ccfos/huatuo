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

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
)

type executor struct {
	process *executil.Process
}

func newExecutor(process *executil.Process) *executor {
	return &executor{process: process}
}

func (e *executor) Start(ctx context.Context) error {
	if err := e.process.Start(ctx); err != nil {
		closeErr := e.process.Close()
		if !errors.Is(err, executil.ErrProcessAlreadyStarted) {
			select {
			case <-e.process.Done():
				err = e.process.Wait()
			default:
			}
		}
		return e.withOutput("start profiler", errors.Join(err, closeErr))
	}
	return nil
}

func (e *executor) Wait() error {
	err := e.process.Wait()
	if err == executil.ErrStopped { //nolint:errorlint // Wrapped or joined stop markers can contain independent failures.
		err = errors.Join(operation.ErrStopped, err)
	}
	return e.withOutput("wait for profiler", err)
}

func (e *executor) Stop(ctx context.Context) error {
	return e.withOutput("stop profiler", e.process.Stop(ctx))
}

func (e *executor) Finalize(ctx context.Context, mode operation.FinalizeMode) (err error) {
	defer func() {
		err = errors.Join(err, e.process.Close())
	}()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("finalize profiler: %w", err)
	}
	switch mode {
	case operation.FinalizePublish, operation.FinalizeDiscard:
		// The CLI waits for the commit ACK before exiting successfully.
		return nil
	default:
		return fmt.Errorf("finalize profiler: unsupported mode %d", mode)
	}
}

func (e *executor) withOutput(action string, err error) error {
	if err == nil {
		return nil
	}
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
