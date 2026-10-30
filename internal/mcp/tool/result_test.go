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

package tool

import (
	"encoding/json"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestBoundedTextResultChecksFinalEncodedSize(t *testing.T) {
	text := strings.Repeat(`"\\\n`, 16)
	encoded, err := json.Marshal(mcpgo.NewToolResultText(text))
	if err != nil {
		t.Fatal(err)
	}
	result, err := boundedTextResult(text, len(encoded))
	if err != nil || result.IsError {
		t.Fatalf("boundedTextResult(limit) = %+v, %v", result, err)
	}
	result, err = boundedTextResult(text, len(encoded)-1)
	if err != nil || !result.IsError {
		t.Fatalf("boundedTextResult(limit+1) = %+v, %v", result, err)
	}
}
