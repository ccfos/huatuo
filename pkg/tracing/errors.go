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

package tracing

import (
	"errors"
	"fmt"
)

var (
	// ErrTracerNotFound indicates that no tracer is registered under a name.
	ErrTracerNotFound = errors.New("tracer not found")
	// ErrTracerAlreadyRunning indicates that a tracer is already active.
	ErrTracerAlreadyRunning = errors.New("tracer already running")
	// ErrTracerNotRunning indicates that a tracer is inactive.
	ErrTracerNotRunning = errors.New("tracer not running")
	// ErrTracerRunErrorPending indicates that a completed run error has not
	// yet been collected by StopByName or Close.
	ErrTracerRunErrorPending = errors.New("tracer run error pending")
	// ErrInvalidTracer indicates that a tracing registration is invalid.
	ErrInvalidTracer = errors.New("invalid tracer")
	// ErrManagerClosed indicates that the manager no longer accepts starts.
	ErrManagerClosed = errors.New("manager closed")
)

func newTracerStateError(err error, name string) error {
	return fmt.Errorf("%q: %w", name, err)
}

func newTracerContextError(operation, name string, err error) error {
	return fmt.Errorf("%s %q: %w", operation, name, err)
}

func newTracerRunError(name string, err error) error {
	return fmt.Errorf("tracer %q: %w", name, err)
}

func newTracerRunErrorPending(name string) error {
	return fmt.Errorf(
		"%q: %w; stop the tracer to collect the previous error before restarting",
		name,
		ErrTracerRunErrorPending,
	)
}
