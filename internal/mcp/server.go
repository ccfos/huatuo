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

// Package mcp exposes HuaTuo diagnostics through Model Context Protocol.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/knowledge"
	"github.com/ccfos/huatuo/internal/mcp/tool"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus"
)

// Config controls protocol-level limits. Backend-specific settings belong to
// the packages that own those backends.
type Config struct {
	MaxConcurrentRequests int
	MaxQueryTimeRange     time.Duration
	MaxRegionsPerQuery    int
	KnowledgeOverlayDir   string
	Events                event.Reader
	EventRegions          []tool.EventRegion
	EventConfig           tool.EventConfig
	Prometheus            tool.PrometheusConfig
	Registerer            prometheus.Registerer
	ToolMetrics           *tool.Metrics
}

// Server owns the MCP protocol server and its HTTP transport.
type Server struct {
	http          *mcpserver.StreamableHTTPServer
	concurrent    chan struct{}
	streams       chan struct{}
	writeTimeout  time.Duration
	metrics       *metrics
	mu            sync.Mutex
	stopping      bool
	cancels       map[context.Context]context.CancelFunc
	active        sync.WaitGroup
	closeBackends func(context.Context) error
}

// NewServer creates an MCP server with stateless HTTP transport. Stateless
// operation avoids retaining abandoned client sessions in API server memory.
func NewServer(cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("MCP config is required")
	}
	maxConcurrent := cfg.MaxConcurrentRequests
	if maxConcurrent <= 0 {
		maxConcurrent = constant.MaxConcurrent
	}
	if maxConcurrent > constant.MaxConcurrent || cfg.MaxRegionsPerQuery > constant.MaxRegions {
		return nil, errors.New("MCP concurrency or region limit exceeds supported maximum")
	}

	toolMetrics := cfg.ToolMetrics
	if toolMetrics == nil {
		var err error
		toolMetrics, err = tool.NewMetrics(cfg.Registerer)
		if err != nil {
			return nil, fmt.Errorf("register MCP tool metrics: %w", err)
		}
	}
	dependencies := tool.Dependencies{
		Events:       cfg.Events,
		EventRegions: cfg.EventRegions,
		EventConfig:  cfg.EventConfig,
		Prometheus:   cfg.Prometheus,
		QueryLimits: tool.QueryLimits{
			MaxQueryTimeRange:  cfg.MaxQueryTimeRange,
			MaxRegionsPerQuery: cfg.MaxRegionsPerQuery,
		},
		Metrics: toolMetrics,
	}
	maxStreams := max(1, maxConcurrent/constant.StreamCapacityDivisor)
	metrics, err := newMetrics(cfg.Registerer, maxConcurrent, maxStreams)
	if err != nil {
		return nil, fmt.Errorf("register MCP metrics: %w", err)
	}
	protocol := mcpserver.NewMCPServer(constant.ServerName, constant.ServerVersion, mcpserver.WithHooks(metrics.hooks()))
	knowledge.Register(protocol, cfg.KnowledgeOverlayDir, cfg.MaxQueryTimeRange, cfg.MaxRegionsPerQuery, toolMetrics.Wrap)
	closeBackends, err := tool.Register(protocol, &dependencies)
	if err != nil {
		return nil, fmt.Errorf("register MCP tools: %w", err)
	}
	toolTimeout := max(cfg.EventConfig.ToolTimeout, cfg.Prometheus.ToolTimeout)
	if toolTimeout <= 0 {
		toolTimeout = constant.DefaultPromDuration
	}
	return &Server{
		http: mcpserver.NewStreamableHTTPServer(
			protocol,
			mcpserver.WithStateLess(true),
		),
		concurrent:    make(chan struct{}, maxConcurrent),
		streams:       make(chan struct{}, maxStreams),
		writeTimeout:  toolTimeout + constant.MCPWriteTimeoutGrace,
		metrics:       metrics,
		closeBackends: closeBackends,
	}, nil
}

// ServeHTTP applies a process-local concurrency bound before dispatching an
// authenticated request to the MCP transport.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w) //nolint:bodyclose // This wraps a response writer, not a response body.
	if r.Method == http.MethodGet {
		// Notification streams outlive the enclosing HTTP server's write budget.
		_ = controller.SetWriteDeadline(time.Time{})
	} else if s.writeTimeout > 0 {
		_ = controller.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		http.Error(w, "MCP server is stopping", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	if s.cancels == nil {
		s.cancels = make(map[context.Context]context.CancelFunc)
	}
	s.cancels[ctx] = cancel
	s.active.Add(1)
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancels, ctx)
		s.mu.Unlock()
		s.active.Done()
	}()
	r = r.WithContext(ctx)
	select {
	case s.concurrent <- struct{}{}:
		defer func() { <-s.concurrent }()
	case <-r.Context().Done():
		return
	default:
		s.metrics.rejected.WithLabelValues(constant.ConcurrencyKindTotal).Inc()
		http.Error(w, "MCP concurrency limit reached", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodGet {
		// Keep notification streams within the total request limit and reserve
		// most capacity for finite Tool calls.
		select {
		case s.streams <- struct{}{}:
			defer func() { <-s.streams }()
		case <-r.Context().Done():
			return
		default:
			s.metrics.rejected.WithLabelValues(constant.ConcurrencyKindStream).Inc()
			http.Error(w, "MCP stream concurrency limit reached", http.StatusServiceUnavailable)
			return
		}
	}
	kind := constant.RequestKindRPC
	if r.Method == http.MethodGet {
		kind = constant.RequestKindStream
	}
	s.metrics.active.WithLabelValues(kind).Inc()
	defer s.metrics.active.WithLabelValues(kind).Dec()

	s.http.ServeHTTP(w, r)
}

// Shutdown stops active transport streams.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	for _, cancel := range s.cancels {
		cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		var backendErr error
		if s.closeBackends != nil {
			backendErr = s.closeBackends(ctx)
		}
		return errors.Join(s.http.Shutdown(ctx), backendErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}
