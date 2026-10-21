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
	"bytes"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestTextSnapshotKeepsDynamicFieldsOnRows(t *testing.T) {
	var output bytes.Buffer
	printIOTracingSnapshot(&output, &types.IOTracingSnapshot{
		Processes: []types.ProcessFileIOStats{{
			PID: 42, Comm: "audit\nforged",
			TotalFiles: []types.FileIOStats{{Path: "/tmp/a\nb"}},
		}},
	})
	if !strings.Contains(output.String(), `audit\nforged`) || !strings.Contains(output.String(), `/tmp/a\nb`) {
		t.Fatalf("missing escaped fields: %q", output.String())
	}
	if strings.Contains(output.String(), "audit\nforged") || strings.Contains(output.String(), "/tmp/a\nb") {
		t.Fatalf("text rows contain raw line breaks in command or path: %q", output.String())
	}
}
