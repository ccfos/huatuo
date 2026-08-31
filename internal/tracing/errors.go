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
	ErrTracerNotFound       = errors.New("tracer not found")
	ErrTracerAlreadyRunning = errors.New("tracer already running")
	ErrTracerNotRunning     = errors.New("tracer not running")
	ErrInvalidTracer        = errors.New("invalid tracer")
	ErrManagerClosed        = errors.New("manager closed")
)

func newTracerStateError(err error, name string) error {
	return fmt.Errorf("%q: %w", name, err)
}

func newTracerContextError(operation, name string, err error) error {
	return fmt.Errorf("%s %q: %w", operation, name, err)
}
