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

package executil

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	defaultStopGracePeriod = 5 * time.Second
	defaultMaxOutputBytes  = 64 << 10
)

// Spec describes one external command invocation.
type Spec struct {
	Path string
	Args []string
	// Env replaces the child environment. A nil Env inherits the parent environment.
	Env []string
	// StopGracePeriod controls when Stop and Run escalate from SIGTERM to SIGKILL.
	// A zero value uses five seconds.
	StopGracePeriod time.Duration
	// MaxOutputBytes limits retained standard output. A zero value uses 64 KiB.
	MaxOutputBytes int
}

// Option configures a process before it starts.
type Option func(*Process)

// WithExtraFiles passes files to the child as descriptors starting at 3.
// The caller owns the files and must keep them open until Start returns.
func WithExtraFiles(files ...*os.File) Option {
	return func(process *Process) {
		process.extraFiles = slices.Clone(files)
	}
}

func (s *Spec) validate() error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("command path must not be empty")
	}

	if strings.IndexByte(s.Path, 0) >= 0 {
		return fmt.Errorf("command path %q contains a null byte", s.Path)
	}

	if err := validateArgs(s.Args); err != nil {
		return err
	}

	for index, value := range s.Env {
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("command environment entry %d contains a null byte", index)
		}

		if strings.IndexByte(value, '=') <= 0 {
			return fmt.Errorf(
				"command environment entry %d must use a non-empty KEY=VALUE form",
				index,
			)
		}
	}

	if s.StopGracePeriod < 0 {
		return errors.New("stop grace period must not be negative")
	}

	if s.MaxOutputBytes < 0 {
		return errors.New("maximum output bytes must not be negative")
	}

	return nil
}

func validateArgs(args []string) error {
	for index, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("command argument %d contains a null byte", index)
		}
	}

	return nil
}
