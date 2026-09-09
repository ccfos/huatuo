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

package exec

import (
	"errors"
	"fmt"
	"strings"
)

const maxOutputInError = 4096

// Result contains profiler-specific data for one target process.
type Result struct {
	PID         int
	Command     string
	Output      []byte
	Diagnostics []byte
	Err         error
}

// Succeeded reports whether the profiler command completed successfully.
func (r *Result) Succeeded() bool {
	return r.Err == nil
}

type resultError struct {
	pid         int
	command     string
	output      string
	diagnostics string
	err         error
}

func (e *resultError) Error() string {
	var message strings.Builder
	fmt.Fprintf(&message, "command %q failed for pid %d", e.command, e.pid)
	if e.err != nil {
		fmt.Fprintf(&message, ": %v", e.err)
	}
	if e.diagnostics != "" {
		fmt.Fprintf(&message, "; diagnostics=%q", e.diagnostics)
	}
	if e.output != "" {
		fmt.Fprintf(&message, "; output=%q", e.output)
	}
	return message.String()
}

func (e *resultError) Unwrap() error {
	return e.err
}

func outputForError(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if len(trimmed) <= maxOutputInError {
		return trimmed
	}
	return trimmed[:maxOutputInError] + "... (truncated)"
}

// Verify reports every failed profiler command with its target and diagnostics.
func Verify(results []*Result) error {
	failures := make([]error, 0)
	for _, result := range results {
		if result.Succeeded() {
			continue
		}
		failures = append(failures, &resultError{
			pid:         result.PID,
			command:     result.Command,
			output:      outputForError(result.Output),
			diagnostics: outputForError(result.Diagnostics),
			err:         result.Err,
		})
	}
	return errors.Join(failures...)
}
