// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package hccn

import (
	"errors"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/executil"
)

func TestGetInfoFromHccnToolInvalidArguments(t *testing.T) {
	output, err := getInfoFromHccnTool("\x00")
	var runErr *executil.RunError
	if !errors.As(err, &runErr) || runErr.ExecutionErr == nil {
		t.Fatalf("getInfoFromHccnTool() error = %v, want configuration error", err)
	}
	if output != "" {
		t.Fatalf("getInfoFromHccnTool() output = %q, want empty output", output)
	}
	for _, want := range []string{"command argument 0 contains a null byte", `stdout=""`, `stderr=""`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("getInfoFromHccnTool() error = %v, want containing %q", err, want)
		}
	}
}
