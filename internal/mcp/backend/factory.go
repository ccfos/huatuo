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

// Package backend selects configured MCP read adapters without modifying the
// application storage framework or retaining mutable global registrations.
package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/mcp/backend/escompatible"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/strutil"
)

type EventConfig struct {
	Type         string
	ESCompatible escompatible.Connection
}

// EventRuntimeOptions keeps charging/observability callbacks out of decoded
// backend connection configuration. Kind is the selected adapter's identity.
type EventRuntimeOptions struct {
	BeforeRequest func(ctx context.Context, kind string) error
}

func normalizeEventKind(kind string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = constant.EventBackendElasticsearch
	}
	switch kind {
	case constant.EventBackendElasticsearch, constant.EventBackendOpenSearch:
		return kind, nil
	default:
		return "", fmt.Errorf("unsupported MCP event backend %q; supported types: elasticsearch, opensearch", kind)
	}
}

// ResolveEventConfig dispatches connection inheritance and validation by
// adapter within MCP. Defaults must come from MCP, not application storage.
func ResolveEventConfig(input, defaults *EventConfig) (EventConfig, error) {
	if input == nil {
		return EventConfig{}, fmt.Errorf("MCP event backend configuration is required")
	}
	resolved := *input
	kind := input.Type
	if kind == "" && defaults != nil {
		kind = defaults.Type
	}
	var err error
	resolved.Type, err = normalizeEventKind(kind)
	if err != nil {
		return EventConfig{}, err
	}
	switch resolved.Type {
	case constant.EventBackendElasticsearch, constant.EventBackendOpenSearch:
		var fallback *escompatible.Connection
		if defaults != nil {
			defaultKind, err := normalizeEventKind(defaults.Type)
			if err != nil {
				return EventConfig{}, err
			}
			if defaultKind == constant.EventBackendElasticsearch || defaultKind == constant.EventBackendOpenSearch {
				fallback = &defaults.ESCompatible
			}
		}
		resolved.ESCompatible = input.ESCompatible.Inherit(fallback)
		if err := resolved.ESCompatible.Validate(); err != nil {
			return EventConfig{}, err
		}
	}
	return resolved, nil
}

func EventReaderConfigured(cfg *EventConfig) bool {
	if cfg == nil {
		return false
	}
	switch cfg.Type {
	case constant.EventBackendElasticsearch, constant.EventBackendOpenSearch:
		return cfg.ESCompatible.Enabled()
	default:
		return false
	}
}

func NewEventReader(cfg *EventConfig, options *EventRuntimeOptions) (event.ManagedReader, error) {
	resolved, err := ResolveEventConfig(cfg, nil)
	if err != nil {
		return nil, err
	}
	kind := resolved.Type
	switch kind {
	case constant.EventBackendElasticsearch, constant.EventBackendOpenSearch:
		connection := resolved.ESCompatible
		var beforeRequest func(context.Context) error
		if options != nil && options.BeforeRequest != nil {
			beforeRequest = func(ctx context.Context) error { return options.BeforeRequest(ctx, kind) }
		}
		return escompatible.New(&escompatible.Config{
			BackendType: kind, Addresses: strutil.SplitCommaList(connection.Address),
			Username: connection.Username, Password: connection.Password, Index: connection.Index,
			BeforeRequest: beforeRequest,
		})
	default:
		return nil, fmt.Errorf("unsupported MCP event backend %q; supported types: elasticsearch, opensearch", kind)
	}
}
