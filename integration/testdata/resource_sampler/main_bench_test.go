//go:build linux

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

package main

import (
	"context"
	"os"
	"testing"

	"github.com/prometheus/procfs"
)

func BenchmarkReadSample(b *testing.B) {
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		b.Fatal(err)
	}
	proc, err := fs.Proc(os.Getpid())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for b.Loop() {
		if _, err := readSample(ctx, proc); err != nil {
			b.Fatal(err)
		}
	}
}
