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
	"context"
	"errors"
	"os/exec"
	"testing"
)

func BenchmarkRun(b *testing.B) {
	b.Run("success", func(b *testing.B) {
		ctx := b.Context()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Run(ctx, Spec{Path: "/bin/true"}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("nonzero exit", func(b *testing.B) {
		ctx := b.Context()
		b.ReportAllocs()
		for b.Loop() {
			_, err := Run(ctx, Spec{Path: "/bin/sh", Args: []string{"-c", "exit 7"}})
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
				b.Fatalf("Run(): %v, want exit code 7", err)
			}
		}
	})
	b.Run("canceled launch", func(b *testing.B) {
		ctx, cancel := context.WithCancel(b.Context())
		cancel()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Run(ctx, Spec{Path: "/bin/true"}); !errors.Is(err, ErrRunCanceled) {
				b.Fatalf("Run(): %v, want cancellation", err)
			}
		}
	})
}
