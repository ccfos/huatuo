// Copyright 2025, 2026 The HuaTuo Authors
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

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/version"

	"github.com/cloudflare/backoff"
	"github.com/gin-contrib/pprof"
	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

// Config defines the configuration options for the HTTP server.
type Config struct {
	EnablePProf     bool
	EnableRateLimit bool
	RateLimit       rate.Limit
	RateBurst       int
	EnableRetry     bool
	AuthUsers       []UserConfig
	PromReg         *prometheus.Registry
	Group           string
	VersionInfo     *version.Info
}

var defaultConfig = &Config{
	EnablePProf:     false,
	EnableRateLimit: false,
	RateLimit:       200,
	RateBurst:       200,
	EnableRetry:     false,
	PromReg:         nil,
	Group:           "",
}

// Server is an HTTP server instance.
type server struct {
	engine       *httpGin.Engine
	promRegistry *prometheus.Registry
	rootGroup    *routerGroup
	mu           sync.Mutex
	httpServer   *http.Server
	listener     net.Listener
	serveErr     chan error
}

// Start binds addr before returning and serves requests in the background.
func (s *server) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	s.mu.Lock()
	if s.httpServer != nil {
		s.mu.Unlock()
		_ = listener.Close()
		return errors.New("http server already started")
	}

	httpServer := &http.Server{
		Handler:           s.engine,
		ReadHeaderTimeout: 30 * time.Second,
	}
	serveErr := make(chan error, 1)
	s.httpServer = httpServer
	s.listener = listener
	s.serveErr = serveErr
	s.mu.Unlock()

	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	return nil
}

// Shutdown stops accepting requests and waits for the serving goroutine.
func (s *server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	httpServer := s.httpServer
	serveErr := s.serveErr
	s.mu.Unlock()
	if httpServer == nil {
		return nil
	}

	shutdownErr := httpServer.Shutdown(ctx)
	var serveResult error
	select {
	case serveResult = <-serveErr:
	case <-ctx.Done():
		serveResult = ctx.Err()
	}

	s.mu.Lock()
	s.httpServer = nil
	s.listener = nil
	s.serveErr = nil
	s.mu.Unlock()

	return errors.Join(shutdownErr, serveResult)
}

type Option struct {
	RetryMaxTime  time.Duration
	RetryInterval time.Duration
	Addr          string
}

// NewServer creates a new HTTP server with the given configuration.
func NewServer(cfg *Config) *server {
	httpGin.SetMode(httpGin.ReleaseMode)

	if cfg == nil {
		cfg = defaultConfig
	}

	s := &server{
		engine:       httpGin.New(),
		promRegistry: cfg.PromReg,
	}

	middleWares := []httpGin.HandlerFunc{
		middlewareContext(),
		requestLogMiddleware(),
		httpGin.Recovery(),
	}
	if cfg.PromReg != nil {
		middleWares = append(middleWares, newHTTPMetricsMiddleware(cfg.PromReg))
	}

	if len(cfg.AuthUsers) > 0 {
		svc := NewAuthService(cfg.AuthUsers)
		middleWares = append(middleWares, wrapHandler(NewAuthMiddleware(svc)))
	}

	if cfg.EnableRateLimit {
		middleWares = append(middleWares, newRateLimitMiddleware(cfg.RateLimit, cfg.RateBurst))
	}

	s.engine.Use(middleWares...)
	if cfg.EnablePProf {
		pprof.Register(s.engine)
	}
	s.rootGroup = NewRoot(s.engine, cfg.Group)
	s.MustRegisterRoutes("", []Handle{
		{Typ: HttpGet, Uri: "/healthz", Handle: s.healthzHandler()},
		{Typ: HttpGet, Uri: "/metrics", Handle: s.promServerHandler()},
	})
	if cfg.VersionInfo != nil {
		s.MustRegisterRoutes("", []Handle{
			{Typ: HttpGet, Uri: "/version", Handle: newVersionHandler(cfg.VersionInfo)},
		})
	}
	return s
}

func requestLogMiddleware() httpGin.HandlerFunc {
	return func(ctx *httpGin.Context) {
		startedAt := time.Now()
		ctx.Next()
		log.WithField("method", ctx.Request.Method).
			WithField("path", ctx.FullPath()).
			WithField("status", ctx.Writer.Status()).
			WithField("latency", time.Since(startedAt)).
			Info("http request completed")
	}
}

func newHTTPMetricsMiddleware(reg prometheus.Registerer) httpGin.HandlerFunc {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "huatuo",
		Subsystem: "http_server",
		Name:      "requests_total",
		Help:      "Total API requests by route, method, and status.",
	}, []string{"route", "method", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "huatuo",
		Subsystem: "http_server",
		Name:      "request_duration_seconds",
		Help:      "API request duration by route and method.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route", "method"})
	reg.MustRegister(requests, duration)

	return func(ctx *httpGin.Context) {
		startedAt := time.Now()
		ctx.Next()
		route := ctx.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(ctx.Writer.Status())
		requests.WithLabelValues(route, ctx.Request.Method, status).Inc()
		duration.WithLabelValues(route, ctx.Request.Method).Observe(time.Since(startedAt).Seconds())
	}
}

func (s *server) healthzHandler() ErrHandlerContextFunc {
	return func(ctx *Context) error {
		ctx.Status(http.StatusNoContent)
		return nil
	}
}

func (s *server) promServerHandler() ErrHandlerContextFunc {
	if s.promRegistry == nil {
		return func(ctx *Context) error {
			ctx.JSON(http.StatusNotImplemented, map[string]any{"status": "Prometheus registry not supported now"})
			return nil
		}
	}

	h := promhttp.HandlerFor(s.promRegistry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Timeout:       30 * time.Second,
	})
	return func(ctx *Context) error {
		h.ServeHTTP(ctx.Writer(), ctx.Request())
		return nil
	}
}

// a middleware for global rate limiting.
func newRateLimitMiddleware(r rate.Limit, burst int) httpGin.HandlerFunc {
	limiter := rate.NewLimiter(r, burst)
	return func(c *httpGin.Context) {
		if !limiter.Allow() {
			ctx := internalContext(c)
			ctx.JSON(http.StatusTooManyRequests, map[string]any{
				"code":    429,
				"message": "too many requests",
				"data":    nil,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// Group return the cgroup for this httpserver
func (s *server) Group() *routerGroup {
	return s.rootGroup
}

const (
	HttpPost   = 1
	HttpDelete = 2
	HttpGet    = 3
	HttpPut    = 4
	HttpPatch  = 5
)

type Handle struct {
	Typ    int
	Uri    string
	Handle ErrHandlerContextFunc
}

func (s *server) MustRegisterRoutes(subGroup string, handlers []Handle) {
	var g *routerGroup = s.rootGroup

	if subGroup != "" {
		g = s.rootGroup.Group(subGroup)
	}

	for _, h := range handlers {
		switch h.Typ {
		case HttpPost:
			g.POST(h.Uri, h.Handle)
		case HttpDelete:
			g.DELETE(h.Uri, h.Handle)
		case HttpGet:
			g.GET(h.Uri, h.Handle)
		case HttpPut:
			g.PUT(h.Uri, h.Handle)
		case HttpPatch:
			g.PATCH(h.Uri, h.Handle)
		default:
			panic("unknown type")
		}
	}
}

func (s *server) run(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %w", err)
	}

	return s.engine.RunListener(listener)
}

// Run starts the TCP server with retry mechanism.
func (s *server) Run(option *Option) error {
	if option.RetryMaxTime > 0 && option.RetryInterval > 0 {
		go func() {
			b := backoff.New(option.RetryMaxTime, option.RetryInterval)
			for {
				err := s.run(option.Addr)
				if err == nil {
					return
				}

				retryInterval := b.Duration()
				if errors.Is(err, syscall.EADDRINUSE) {
					log.WithError(err).WithField("retry_interval", retryInterval).
						Info("tcp api address is in use; retrying")
				} else if err != nil {
					log.WithError(err).WithField("retry_interval", retryInterval).
						Warn("tcp api server failed; retrying")
				}
				time.Sleep(retryInterval)
			}
		}()
		return fmt.Errorf("init err")
	}

	return s.run(option.Addr)
}
