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
	"errors"
	"fmt"
	"time"

	huatuomcp "github.com/ccfos/huatuo/internal/mcp"
	"github.com/ccfos/huatuo/internal/mcp/backend"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/tool"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

func setupMCP(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	if !mcpEnabled(d.opts) {
		return nil, nil
	}

	users := utils.NewPrincipalRateBudgets(d.opts.Config.MCP.UserRateLimit)
	eventSettings := d.opts.Config.MCP.Events
	eventLimiter := utils.NewRateLimiterWithUsers(eventSettings.BackendRateLimit, users)
	promLimiter := utils.NewRateLimiterWithUsers(d.opts.Config.MCP.Prometheus.BackendRateLimit, users)
	toolMetrics, err := tool.NewMetrics(d.metrics)
	if err != nil {
		return nil, fmt.Errorf("register MCP tool metrics: %w", err)
	}
	promLimiter.SetRateLimitObserver(func(scope string) {
		toolMetrics.RateLimitRejected(constant.BackendPrometheus, scope)
	})
	options := backend.EventRuntimeOptions{BeforeRequest: func(ctx context.Context, kind string) error {
		err := eventLimiter.AllowBackendRequest(ctx)
		var rejection *utils.RateLimitError
		if errors.As(err, &rejection) {
			toolMetrics.RateLimitRejected(kind, rejection.Scope)
		}
		return err
	}}
	events, eventRegions, ownedEventStores, err := newMCPEventStores(ctx, d, &options)
	if err != nil {
		return nil, err
	}
	cleanupStores := func(ctx context.Context) error {
		var errs []error
		for _, store := range ownedEventStores {
			errs = append(errs, store.Close(ctx))
		}
		return errors.Join(errs...)
	}

	promRegions := make([]tool.PrometheusRegionConfig, 0, len(d.opts.Config.MCP.Prometheus.Regions))
	for _, region := range d.opts.Config.MCP.Prometheus.Regions {
		promRegions = append(promRegions, tool.PrometheusRegionConfig{
			NamePattern:  region.NamePattern,
			Address:      optionalMCPString(region.Address),
			AuthType:     region.AuthType,
			AuthToken:    region.AuthToken,
			AuthUser:     region.AuthUser,
			AuthPass:     region.AuthPass,
			MetricPrefix: region.MetricPrefix,
		})
	}
	promConfig := d.opts.Config.MCP.Prometheus
	server, err := huatuomcp.NewServer(&huatuomcp.Config{
		MaxConcurrentRequests: d.opts.Config.MCP.MaxConcurrentRequests,
		MaxQueryTimeRange:     time.Duration(d.opts.Config.MCP.MaxQueryTimeRange) * time.Second,
		MaxRegionsPerQuery:    d.opts.Config.MCP.MaxRegionsPerQuery,
		KnowledgeOverlayDir:   d.opts.Config.MCP.KnowledgeOverlayDir,
		Events:                events,
		EventRegions:          eventRegions,
		EventConfig: tool.EventConfig{
			MaxDocumentsPerQuery: eventSettings.MaxDocumentsPerQuery,
			ToolTimeout:          time.Duration(eventSettings.ToolTimeout) * time.Second,
		},
		Registerer:  d.metrics,
		ToolMetrics: toolMetrics,
		Prometheus: tool.PrometheusConfig{
			Address:                promConfig.Address,
			AuthType:               promConfig.AuthType,
			AuthToken:              promConfig.AuthToken,
			AuthUser:               promConfig.AuthUser,
			AuthPass:               promConfig.AuthPass,
			MetricPrefix:           promConfig.MetricPrefix,
			MaxSeriesPerQuery:      promConfig.MaxSeriesPerQuery,
			MaxMetricsPerDiscovery: promConfig.MaxMetricsPerDiscovery,
			ToolTimeout:            time.Duration(promConfig.ToolTimeout) * time.Second,
			Regions:                promRegions,
			Limiter:                promLimiter,
		},
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create MCP server: %w", err), cleanupStores(ctx))
	}
	d.mcpServer = server
	return func(ctx context.Context) error {
		return errors.Join(d.mcpServer.Shutdown(ctx), cleanupStores(ctx))
	}, nil
}

func mcpEnabled(opts *Options) bool {
	return opts != nil && (opts.EnableMCP || opts.Config != nil && opts.Config.MCP.Enabled)
}

func newMCPEventStores(
	ctx context.Context,
	d *Daemon,
	options *backend.EventRuntimeOptions,
) (event.Reader, []tool.EventRegion, []event.ManagedReader, error) {
	settings := d.opts.Config.MCP.Events
	input := settings.ReaderConfig()
	defaultConfig, err := backend.ResolveEventConfig(&input, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve default MCP event reader: %w", err)
	}
	if !backend.EventReaderConfigured(&defaultConfig) {
		return nil, nil, nil, nil
	}
	defaultStore, err := backend.NewEventReader(&defaultConfig, options)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("initialize default MCP event store: %w", err)
	}
	owned := []event.ManagedReader{defaultStore}
	regions := make([]tool.EventRegion, 0, len(settings.Regions))
	for _, region := range settings.Regions {
		override := region.ReaderConfig()
		regionConfig, err := backend.ResolveEventConfig(&override, &defaultConfig)
		var store event.ManagedReader
		if err == nil {
			store, err = backend.NewEventReader(&regionConfig, options)
		}
		if err != nil {
			for _, initialized := range owned {
				_ = initialized.Close(context.WithoutCancel(ctx))
			}
			return nil, nil, nil, fmt.Errorf(
				"initialize MCP event store for region %q: %w",
				region.NamePattern,
				err,
			)
		}
		owned = append(owned, store)
		regions = append(regions, tool.EventRegion{NamePattern: region.NamePattern, Store: store})
	}
	return defaultStore, regions, owned, nil
}

func optionalMCPString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
