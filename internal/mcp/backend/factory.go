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

// Package backend constructs MCP read adapters without modifying the
// application storage framework or retaining mutable global registrations.
package backend

import (
	"context"
	"fmt"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/backend/storage"
	"github.com/ccfos/huatuo/internal/strutil"
)

type EventConfig struct {
	Storage storage.Connection
}

// EventRuntimeOptions keeps charging/observability callbacks out of decoded
// backend connection configuration. Kind identifies the shared event protocol.
type EventRuntimeOptions struct {
	BeforeRequest func(ctx context.Context, kind string) error
}

// ResolveEventConfig normalizes and validates one independent regional reader.
func ResolveEventConfig(input *EventConfig) (EventConfig, error) {
	if input == nil {
		return EventConfig{}, fmt.Errorf("MCP event backend configuration is required")
	}
	resolved := *input
	if resolved.Storage.AuthType == "none" {
		resolved.Storage.Username, resolved.Storage.Password = "", ""
	}
	if err := resolved.Storage.Validate(); err != nil {
		return EventConfig{}, err
	}
	return resolved, nil
}

func EventReaderConfigured(cfg *EventConfig) bool {
	return cfg != nil && cfg.Storage.Enabled()
}

func NewEventReader(cfg *EventConfig, options *EventRuntimeOptions) (event.ManagedReader, error) {
	resolved, err := ResolveEventConfig(cfg)
	if err != nil {
		return nil, err
	}
	connection := resolved.Storage
	var beforeRequest func(context.Context) error
	if options != nil && options.BeforeRequest != nil {
		beforeRequest = func(ctx context.Context) error {
			return options.BeforeRequest(ctx, event.EventBackendElasticsearch)
		}
	}
	return storage.New(&storage.Config{
		Addresses: strutil.SplitCommaList(connection.Address),
		Username:  connection.Username, Password: connection.Password, Index: connection.Index,
		BeforeRequest: beforeRequest,
	})
}
