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

// Package guide registers HuaTuo diagnostic guidance.
package guide

import (
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ToolDecorator adds cross-cutting behavior to a guide Tool handler.
type ToolDecorator func(string, mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc

// Register exposes the original guide tree and a tool projection for
// clients that cannot consume resources or prompts directly.
func Register(server *mcpserver.MCPServer, dataDir string, maxQueryRange time.Duration, maxRegions int, decorate ToolDecorator) {
	dataFS := NewFS(dataDir)
	RegisterResources(dataFS, server)
	RegisterPrompts(dataFS, server, maxQueryRange, maxRegions)
	registerGuideTool(dataFS, server, decorate)
}
