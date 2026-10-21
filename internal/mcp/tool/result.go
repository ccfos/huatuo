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
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func boundedIndentedJSONResult(value any, maximum int) (*mcpgo.CallToolResult, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode tool result: %w", err)
	}
	return boundedTextResult(string(data), maximum)
}

func boundedTextResult(text string, maximum int) (*mcpgo.CallToolResult, error) {
	result := mcpgo.NewToolResultText(text)
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode tool result envelope: %w", err)
	}
	if len(encoded) > maximum {
		return mcpgo.NewToolResultError(fmt.Sprintf("result exceeds %d bytes; reduce query scope", maximum)), nil
	}
	return result, nil
}

func boundToolResult(result *mcpgo.CallToolResult, maximum int) *mcpgo.CallToolResult {
	if result == nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maximum {
		return mcpgo.NewToolResultError(fmt.Sprintf("result exceeds %d bytes; reduce query scope", maximum))
	}
	return result
}
