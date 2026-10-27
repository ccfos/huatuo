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
	"github.com/ccfos/huatuo/internal/mcp/tool"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

func setupMCP(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	if !mcpEnabled(d.opts) {
		return nil, nil
	}

	eventSettings := d.opts.Config.MCP.Events
	eventLimiter := utils.NewRateLimiter(eventSettings.RateLimit)
	promLimiter := utils.NewRateLimiter(d.opts.Config.MCP.Metrics.RateLimit)
	toolMetrics, err := tool.NewMetrics(d.metrics)
	if err != nil {
		return nil, fmt.Errorf("register MCP tool metrics: %w", err)
	}
	promLimiter.SetRateLimitObserver(func(scope string) {
		toolMetrics.RateLimitRejected(tool.BackendPrometheus, scope)
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

	promRegions := make([]tool.PrometheusRegionConfig, 0, len(d.opts.Config.MCP.Metrics.Regions))
	for _, region := range d.opts.Config.MCP.Metrics.Regions {
		promRegions = append(promRegions, tool.PrometheusRegionConfig{
			NamePattern:  region.NamePattern,
			Address:      region.Address,
			AuthType:     region.AuthType,
			AuthToken:    region.AuthToken,
			AuthUser:     region.AuthUser,
			AuthPass:     region.AuthPass,
			MetricPrefix: region.MetricPrefix,
		})
	}
	metricsConfig := d.opts.Config.MCP.Metrics
	handler, err := huatuomcp.NewHandler(&huatuomcp.Config{
		MaxConcurrentRequests: d.opts.Config.MCP.MaxConcurrentRequests,
		MaxQueryTimeRange:     time.Duration(d.opts.Config.MCP.MaxQueryTimeRange) * time.Second,
		MaxRegionsPerQuery:    d.opts.Config.MCP.MaxRegionsPerQuery,
		GuideOverlayDir:       d.opts.Config.MCP.GuideOverlayDir,
		Events:                events,
		EventRegions:          eventRegions,
		EventConfig: tool.EventConfig{
			MaxDocumentsPerQuery: eventSettings.MaxDocumentsPerQuery,
			ToolTimeout:          time.Duration(eventSettings.ToolTimeout) * time.Second,
		},
		Registerer:  d.metrics,
		ToolMetrics: toolMetrics,
		Prometheus: tool.PrometheusConfig{
			MaxSeriesPerQuery:      metricsConfig.MaxSeriesPerQuery,
			MaxMetricsPerDiscovery: metricsConfig.MaxMetricsPerDiscovery,
			ToolTimeout:            time.Duration(metricsConfig.ToolTimeout) * time.Second,
			Regions:                promRegions,
			Limiter:                promLimiter,
		},
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create MCP handler: %w", err), cleanupStores(ctx))
	}
	d.mcpHandler = handler
	return func(ctx context.Context) error {
		return errors.Join(d.mcpHandler.Shutdown(ctx), cleanupStores(ctx))
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
	var first event.Reader
	owned := make([]event.ManagedReader, 0, len(settings.Regions))
	regions := make([]tool.EventRegion, 0, len(settings.Regions))
	for _, region := range settings.Regions {
		input := region.ReaderConfig()
		regionConfig, err := backend.ResolveEventConfig(&input)
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
		if first == nil {
			first = store
		}
		regions = append(regions, tool.EventRegion{NamePattern: region.NamePattern, Store: store})
	}
	return first, regions, owned, nil
}
