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
	EnablePProf       bool
	EnableRateLimit   bool
	RateLimit         rate.Limit
	RateBurst         int
	EnableRetry       bool
	RequireAuth       bool
	AuthUsers         []UserConfig
	PublicPaths       []string
	AdminPaths        []string
	PromReg           *prometheus.Registry
	Group             string
	VersionInfo       *version.Info
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	Ready             func(context.Context) error
}

var defaultConfig = &Config{
	EnablePProf:       false,
	EnableRateLimit:   false,
	RateLimit:         200,
	RateBurst:         200,
	EnableRetry:       false,
	PromReg:           nil,
	Group:             "",
	ReadHeaderTimeout: 10 * time.Second,
	ReadTimeout:       30 * time.Second,
	WriteTimeout:      60 * time.Second,
	IdleTimeout:       120 * time.Second,
	MaxHeaderBytes:    1 << 20,
	MaxBodyBytes:      4 << 20,
}

// Server is an HTTP server instance.
type server struct {
	engine       *httpGin.Engine
	promRegistry *prometheus.Registry
	rootGroup    *routerGroup
	mu           sync.Mutex
	httpServer   *http.Server
	listener     net.Listener
	serveDone    chan struct{}
	serveResult  error
	config       Config
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
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    s.config.MaxHeaderBytes,
	}
	serveDone := make(chan struct{})
	s.httpServer = httpServer
	s.listener = listener
	s.serveDone = serveDone
	s.serveResult = nil
	s.mu.Unlock()

	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.mu.Lock()
		s.serveResult = err
		close(serveDone)
		s.mu.Unlock()
	}()

	return nil
}

// Shutdown stops accepting requests and waits for the serving goroutine.
func (s *server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	httpServer := s.httpServer
	s.mu.Unlock()
	if httpServer == nil {
		return nil
	}

	shutdownErr := httpServer.Shutdown(ctx)
	serveResult := s.Wait(ctx)

	s.mu.Lock()
	s.httpServer = nil
	s.listener = nil
	s.mu.Unlock()

	return errors.Join(shutdownErr, serveResult)
}

// Done is closed when the serving goroutine exits.
func (s *server) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveDone
}

// Addr returns the bound listener address after Start.
func (s *server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Wait returns the serving result or the context error.
func (s *server) Wait(ctx context.Context) error {
	s.mu.Lock()
	done := s.serveDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.serveResult
	case <-ctx.Done():
		return ctx.Err()
	}
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
	effectiveConfig := *cfg
	normalizeServerConfig(&effectiveConfig)
	cfg = &effectiveConfig

	s := &server{
		engine:       httpGin.New(),
		promRegistry: cfg.PromReg,
		config:       *cfg,
	}

	middleWares := []httpGin.HandlerFunc{
		middlewareContext(),
		maxBodyBytesMiddleware(cfg.MaxBodyBytes),
		requestLogMiddleware(),
		httpGin.Recovery(),
	}
	if cfg.PromReg != nil {
		middleWares = append(middleWares, newHTTPMetricsMiddleware(cfg.PromReg))
	}

	if cfg.RequireAuth || len(cfg.AuthUsers) > 0 {
		svc := NewAuthService(cfg.AuthUsers)
		publicPaths := append([]string{"/healthz", "/readyz", "/metrics", "/version"}, cfg.PublicPaths...)
		adminPaths := append([]string{"/debug/pprof", "/debug/pprof/**"}, cfg.AdminPaths...)
		middleWares = append(middleWares, wrapHandler(NewAuthMiddleware(svc, publicPaths, adminPaths)))
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
		{Typ: HttpGet, Uri: "/readyz", Handle: s.readyzHandler()},
		{Typ: HttpGet, Uri: "/metrics", Handle: s.promServerHandler()},
	})
	if cfg.VersionInfo != nil {
		s.MustRegisterRoutes("", []Handle{
			{Typ: HttpGet, Uri: "/version", Handle: newVersionHandler(cfg.VersionInfo)},
		})
	}
	return s
}

func normalizeServerConfig(cfg *Config) {
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = defaultConfig.ReadHeaderTimeout
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultConfig.ReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultConfig.WriteTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultConfig.IdleTimeout
	}
	if cfg.MaxHeaderBytes <= 0 {
		cfg.MaxHeaderBytes = defaultConfig.MaxHeaderBytes
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultConfig.MaxBodyBytes
	}
}

func maxBodyBytesMiddleware(limit int64) httpGin.HandlerFunc {
	return func(ctx *httpGin.Context) {
		if ctx.Request.Body != nil {
			ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
		}
		ctx.Next()
	}
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

func (s *server) readyzHandler() ErrHandlerContextFunc {
	return func(ctx *Context) error {
		if s.config.Ready == nil {
			ctx.Status(http.StatusNoContent)
			return nil
		}
		if err := s.config.Ready(ctx.Request().Context()); err != nil {
			log.WithError(err).Warn("readiness check failed")
			ctx.JSON(http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return nil
		}
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
	type limiterEntry struct {
		limiter  *rate.Limiter
		lastSeen time.Time
	}
	var mu sync.Mutex
	limiters := make(map[string]limiterEntry)
	var requests uint64
	return func(c *httpGin.Context) {
		key := internalContext(c).UserID
		if key == "" {
			key = c.Request.RemoteAddr
			if host, _, err := net.SplitHostPort(key); err == nil {
				key = host
			}
		}
		now := time.Now()
		mu.Lock()
		entry, exists := limiters[key]
		if !exists {
			entry.limiter = rate.NewLimiter(r, burst)
		}
		entry.lastSeen = now
		limiters[key] = entry
		requests++
		if requests%1000 == 0 {
			for client, candidate := range limiters {
				if now.Sub(candidate.lastSeen) > 10*time.Minute {
					delete(limiters, client)
				}
			}
		}
		allowed := entry.limiter.Allow()
		mu.Unlock()
		if !allowed {
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
