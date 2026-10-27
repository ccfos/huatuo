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
	"context"
	"fmt"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	tracingstore "github.com/ccfos/huatuo/internal/mcp/backend/event"
	mcputils "github.com/ccfos/huatuo/internal/mcp/utils"
)

const (
	QueryScopeTarget = "target"
	QueryScopeRegion = "region"
)

func queryScope(request mcpgo.CallToolRequest) (string, error) {
	value, exists := request.GetArguments()[ArgumentScope]
	if !exists {
		return QueryScopeTarget, nil
	}
	scope, ok := value.(string)
	if !ok || scope != QueryScopeTarget && scope != QueryScopeRegion {
		return "", fmt.Errorf("%s must be %s or %s", ArgumentScope, QueryScopeTarget, QueryScopeRegion)
	}
	return scope, nil
}

// QueryLimits contains protocol-level limits shared by backend query tools.
type QueryLimits struct {
	MaxQueryTimeRange  time.Duration
	MaxRegionsPerQuery int
}

// Dependencies contains optional backends. A tool is exposed only when its
// backend has valid configuration, independent of transient backend health.
type Dependencies struct {
	Events       tracingstore.Reader
	EventRegions []EventRegion
	EventConfig  EventConfig
	Prometheus   PrometheusConfig
	QueryLimits  QueryLimits
	Metrics      *Metrics
}

// Register adds configured tools and returns cleanup for owned backend readers.
func Register(server *mcpserver.MCPServer, dependencies *Dependencies) (func(context.Context) error, error) {
	limits := dependencies.QueryLimits
	if limits.MaxRegionsPerQuery <= 0 {
		limits.MaxRegionsPerQuery = mcputils.MaxRegions
	}
	if dependencies.Events != nil {
		if err := registerEventTools(
			server,
			dependencies.Events,
			dependencies.EventRegions,
			dependencies.EventConfig,
			limits,
			dependencies.Metrics,
		); err != nil {
			return nil, err
		}
	}
	return registerPrometheusTools(server, &dependencies.Prometheus, limits, dependencies.Metrics)
}

func boolPointer(value bool) *bool {
	return &value
}
