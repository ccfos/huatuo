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

package server

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	v1 "github.com/ccfos/huatuo/apis/v1"
	"github.com/ccfos/huatuo/internal/auth"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/server/response"

	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

func buildMiddlewareChain(cfg *Config) []httpGin.HandlerFunc {
	chain := []httpGin.HandlerFunc{
		middlewareContext(cfg.ErrorStatusMapper),
		maxBodyBytesMiddleware(cfg.MaxBodyBytes),
		requestLogMiddleware(),
		newRecoveryMiddleware(cfg.ErrorStatusMapper),
	}
	if cfg.PromReg != nil {
		chain = append(chain, newHTTPMetricsMiddleware(cfg.PromReg))
	}
	publicPaths := append(
		[]string{"/metrics", "/version"},
		cfg.PublicPaths...,
	)
	if len(cfg.AuthTokens) > 0 {
		authenticator := auth.NewTokenAuthenticator(cfg.AuthTokens)
		chain = append(
			chain,
			wrapHandler(newTokenAuthMiddleware(authenticator, publicPaths)),
		)
	}
	if len(cfg.AuthUsers) > 0 {
		authService := NewAuthService(cfg.AuthUsers)
		adminPaths := append(
			[]string{"/debug/pprof", "/debug/pprof/**"},
			cfg.AdminPaths...,
		)
		chain = append(
			chain,
			wrapHandler(NewAuthMiddleware(authService, publicPaths, adminPaths)),
		)
	}
	if cfg.RateLimit != nil {
		chain = append(chain, newRateLimitMiddleware(
			rate.Limit(cfg.RateLimit.RequestsPerSecond),
			cfg.RateLimit.Burst,
		))
	}
	return chain
}

func newRecoveryMiddleware(statusMapper response.HTTPStatusMapper) httpGin.HandlerFunc {
	return httpGin.CustomRecovery(func(ctx *httpGin.Context, _ any) {
		writeGinError(ctx, response.ErrInternal, statusMapper)
	})
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
			Debug("http request completed")
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
	requests = registerOrReuseCounterVec(reg, requests)
	duration = registerOrReuseHistogramVec(reg, duration)

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

func registerOrReuseCounterVec(reg prometheus.Registerer, vec *prometheus.CounterVec) *prometheus.CounterVec {
	if err := reg.Register(vec); err != nil {
		var alreadyRegistered prometheus.AlreadyRegisteredError
		if !errors.As(err, &alreadyRegistered) {
			panic(err)
		}
		existing, ok := alreadyRegistered.ExistingCollector.(*prometheus.CounterVec)
		if !ok {
			panic(err)
		}
		return existing
	}
	return vec
}

func registerOrReuseHistogramVec(reg prometheus.Registerer, vec *prometheus.HistogramVec) *prometheus.HistogramVec {
	if err := reg.Register(vec); err != nil {
		var alreadyRegistered prometheus.AlreadyRegisteredError
		if !errors.As(err, &alreadyRegistered) {
			panic(err)
		}
		existing, ok := alreadyRegistered.ExistingCollector.(*prometheus.HistogramVec)
		if !ok {
			panic(err)
		}
		return existing
	}
	return vec
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
			response.ErrorWithCode(
				ctx,
				ctx.ErrorStatusMapper(),
				v1.ErrorCodeRateLimited,
				"too many requests",
			)
			c.Abort()
			return
		}
		c.Next()
	}
}
