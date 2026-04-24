// Copyright 2025 The HuaTuo Authors
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
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"huatuo-bamai/internal/log"

	"github.com/cloudflare/backoff"
	"github.com/gin-contrib/pprof"
	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

type Config struct {
	EnablePProf     bool
	EnableRateLimit bool
	RateLimit       rate.Limit
	RateBurst       int
	EnableRetry     bool
	AuthUsers       []UserConfig
	PromReg         *prometheus.Registry
	Group           string
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

type server struct {
	engine       *httpGin.Engine
	promRegistry *prometheus.Registry
	rootGroup    *routerGroup
}

type Option struct {
	RetryMaxTime  time.Duration
	RetryInterval time.Duration
	Addr          string
}

func NewServer(cfg *Config) *server {
	httpGin.SetMode(httpGin.ReleaseMode)
	if cfg == nil {
		cfg = defaultConfig
	}

	s := &server{
		engine:       httpGin.New(),
		promRegistry: cfg.PromReg,
	}

	if cfg.EnablePProf {
		pprof.Register(s.engine)
	}

	middleWares := []httpGin.HandlerFunc{
		middlewareContext(),
		httpGin.Logger(),
		httpGin.Recovery(),
	}

	if len(cfg.AuthUsers) > 0 {
		svc := NewAuthService(cfg.AuthUsers)
		middleWares = append(middleWares, wrapHandler(NewAuthMiddleware(svc)))
	}

	if cfg.EnableRateLimit {
		middleWares = append(middleWares, newRateLimitMiddleware(cfg.RateLimit, cfg.RateBurst))
	}

	s.engine.Use(middleWares...)
	s.rootGroup = NewRoot(s.engine, cfg.Group)
	s.MustRegisterRoutes("", []Handle{
		{Typ: HttpGet, Uri: "/metrics", Handle: s.promServerHandler()},
	})
	return s
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

func (s *server) Group() *routerGroup {
	return s.rootGroup
}

const (
	HttpPost   = 1
	HttpDelete = 2
	HttpGet    = 3
	HttpPut    = 4
)

type Handle struct {
	Typ    int
	Uri    string
	Handle ErrHandlerContextFunc
}

func (s *server) MustRegisterRoutes(subGroup string, handlers []Handle) {
	g := s.rootGroup
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

	tcpListener := listener.(*net.TCPListener)
	file, err := tcpListener.File()
	if err != nil {
		return fmt.Errorf("get listener fd %w", err)
	}

	if err := syscall.SetsockoptInt(int(file.Fd()), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return fmt.Errorf("set sockopt addr reuse %w", err)
	}

	return s.engine.RunListener(tcpListener)
}

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
					log.Infof("tcp api server %v, retrying in %v ...", err, retryInterval)
				} else if err != nil {
					log.Warnf("tcp api server %v, retrying in %v ...", err, retryInterval)
				}
				time.Sleep(retryInterval)
			}
		}()
		return fmt.Errorf("init err")
	}

	return s.run(option.Addr)
}
