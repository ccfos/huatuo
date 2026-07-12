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
	"io/fs"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/version"

	"github.com/cloudflare/backoff"
	"github.com/gin-contrib/pprof"
	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 60 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMaxHeaderBytes    = 1 << 20
	defaultMaxBodyBytes      = 4 << 20
)

// Config defines the configuration options for the HTTP server.
type Config struct {
	EnablePProf       bool
	RateLimit         *RateLimitConfig
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

// RateLimitConfig enables per-client rate limiting.
type RateLimitConfig struct {
	RequestsPerSecond int
	Burst             int
}

// ErrServerStopping indicates that the server is already shutting down.
var ErrServerStopping = errors.New("http server is stopping")

type serverState uint8

const (
	serverStateStopped serverState = iota
	serverStateRunning
	serverStateStopping
)

type serveExecution struct {
	httpServer *http.Server
	listener   net.Listener
	done       chan struct{}
	result     error
}

func (e *serveExecution) shutdown(ctx context.Context) error {
	return e.httpServer.Shutdown(ctx)
}

func (e *serveExecution) wait(ctx context.Context) error {
	select {
	case <-e.done:
		return e.result
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Server is an HTTP server instance.
type Server struct {
	engine          *httpGin.Engine
	promRegistry    *prometheus.Registry
	rootGroup       *routerGroup
	mu              sync.Mutex
	state           serverState
	activeExecution *serveExecution
	config          Config
	authSvc         *authService
}

// Start binds addr before returning and serves requests in the background.
func (s *Server) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	httpServer := &http.Server{
		Handler:           s.engine.Handler(),
		ReadHeaderTimeout: s.config.ReadHeaderTimeout,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		IdleTimeout:       s.config.IdleTimeout,
		MaxHeaderBytes:    s.config.MaxHeaderBytes,
	}
	execution := &serveExecution{
		httpServer: httpServer,
		listener:   listener,
		done:       make(chan struct{}),
	}

	s.mu.Lock()
	switch s.state {
	case serverStateStopping:
		s.mu.Unlock()
		_ = listener.Close()
		return ErrServerStopping
	case serverStateRunning:
		s.mu.Unlock()
		_ = listener.Close()
		return errors.New("http server already started")
	}
	s.state = serverStateRunning
	s.activeExecution = execution
	s.mu.Unlock()

	go func() {
		err := execution.httpServer.Serve(execution.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		execution.result = err
		close(execution.done)
	}()

	return nil
}

// Shutdown stops accepting requests and waits for the serving goroutine.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	switch s.state {
	case serverStateStopped:
		s.mu.Unlock()
		return nil
	case serverStateStopping:
		s.mu.Unlock()
		return ErrServerStopping
	}
	execution := s.activeExecution
	s.state = serverStateStopping
	s.mu.Unlock()

	shutdownErr := execution.shutdown(ctx)
	serveResult := execution.wait(ctx)

	s.mu.Lock()
	s.activeExecution = nil
	s.state = serverStateStopped
	s.mu.Unlock()

	return errors.Join(shutdownErr, serveResult)
}

// Done is closed when the serving goroutine exits.
func (s *Server) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeExecution == nil {
		return nil
	}
	return s.activeExecution.done
}

// Wait returns the serving result or the context error.
func (s *Server) Wait(ctx context.Context) error {
	s.mu.Lock()
	execution := s.activeExecution
	s.mu.Unlock()

	if execution == nil {
		return nil
	}
	return execution.wait(ctx)
}

type Option struct {
	RetryMaxTime  time.Duration
	RetryInterval time.Duration
	Addr          string
}

// NewServer creates a new HTTP server with the given configuration.
func NewServer(cfg *Config) *Server {
	httpGin.SetMode(httpGin.ReleaseMode)

	var effectiveConfig Config
	if cfg != nil {
		effectiveConfig = *cfg
	}
	effectiveConfig.applyDefaults()

	s := &Server{
		engine:       httpGin.New(),
		promRegistry: effectiveConfig.PromReg,
		config:       effectiveConfig,
	}
	if effectiveConfig.RequireAuth || len(effectiveConfig.AuthUsers) > 0 {
		s.authSvc = NewAuthService(effectiveConfig.AuthUsers)
	}

	s.engine.Use(buildMiddlewareChain(&effectiveConfig, s.authSvc)...)
	if effectiveConfig.EnablePProf {
		pprof.Register(s.engine)
	}
	s.rootGroup = NewRoot(s.engine, effectiveConfig.Group)
	s.MustRegisterRoutes("", []Route{
		{Method: http.MethodGet, Path: "/healthz", Handler: s.healthzHandler()},
		{Method: http.MethodGet, Path: "/readyz", Handler: s.readyzHandler()},
		{Method: http.MethodGet, Path: "/metrics", Handler: s.metricsHandler()},
	})
	if effectiveConfig.VersionInfo != nil {
		s.MustRegisterRoutes("", []Route{
			{
				Method:  http.MethodGet,
				Path:    "/version",
				Handler: newVersionHandler(effectiveConfig.VersionInfo),
			},
		})
	}
	return s
}

func (c *Config) applyDefaults() {
	if c.ReadHeaderTimeout <= 0 {
		c.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = defaultReadTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = defaultWriteTimeout
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.MaxHeaderBytes <= 0 {
		c.MaxHeaderBytes = defaultMaxHeaderBytes
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = defaultMaxBodyBytes
	}
}

// Group return the cgroup for this httpserver
func (s *Server) Group() *routerGroup {
	return s.rootGroup
}

// MethodAny registers a route for all HTTP methods supported by Gin.
const MethodAny = "*"

// UserManager returns the user/API-key registry used for authentication, or
// nil when authentication is disabled. Callers may type-assert against the
// UserManager interface to administer users and API keys.
func (s *Server) UserManager() UserManager {
	if s.authSvc == nil {
		return nil
	}
	return s.authSvc
}

// StaticFS serves files from fsys at the given URL path using gin's StaticFS.
// relativePath must not contain URL parameters. The handler is registered on
// the root engine so it participates in the normal routing/middleware chain.
func (s *Server) StaticFS(relativePath string, fsys fs.FS) {
	s.engine.StaticFS(relativePath, http.FS(fsys))
}

// Redirect registers a route that performs an HTTP redirect to targetPath.
func (s *Server) Redirect(relativePath, targetPath string, code int) {
	s.engine.GET(relativePath, func(c *httpGin.Context) {
		c.Redirect(code, targetPath)
	})
}

// HTTPHandler returns the underlying http.Handler. Useful for embedding the
// server in another process and for in-process testing with httptest.
func (s *Server) HTTPHandler() http.Handler {
	return s.engine
}

// Route defines an HTTP route.
type Route struct {
	Method  string
	Path    string
	Handler ErrHandlerContextFunc
}

func (s *Server) MustRegisterRoutes(subGroup string, routes []Route) {
	g := s.rootGroup

	if subGroup != "" {
		g = s.rootGroup.Group(subGroup)
	}

	for _, route := range routes {
		switch route.Method {
		case "":
			panic(fmt.Sprintf("route %q has no http method", route.Path))
		case MethodAny:
			g.Any(route.Path, route.Handler)
		default:
			g.Handle(route.Method, route.Path, route.Handler)
		}
	}
}

func (s *Server) run(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %w", err)
	}

	return s.engine.RunListener(listener)
}

// Run starts the TCP server with retry mechanism.
func (s *Server) Run(option *Option) error {
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
