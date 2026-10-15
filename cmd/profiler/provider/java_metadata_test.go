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

package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func TestJavaProcessNamesSkipEmptyRecords(t *testing.T) {
	names, err := javaProcessNames(t.Context(), []profile.SampleOutput{{PID: -1}}, -1)
	if err != nil || len(names) != 0 {
		t.Fatalf("names = %v, error = %v", names, err)
	}
}

func TestJavaProcessNamesPropagateLookupFailure(t *testing.T) {
	_, err := javaProcessNames(t.Context(), []profile.SampleOutput{{PID: -1, Output: "work 1\n"}}, -1)
	if err == nil || !strings.Contains(err.Error(), "PID -1") {
		t.Fatalf("error = %v, want failed PID lookup", err)
	}
}

func TestJavaProcessNamesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := javaProcessNames(ctx, []profile.SampleOutput{{PID: -1, Output: "work 1\n"}}, -1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}
