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
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server/response"

	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

func buildMiddlewareChain(cfg *Config) []httpGin.HandlerFunc {
	chain := []httpGin.HandlerFunc{
		middlewareContext(),
		maxBodyBytesMiddleware(cfg.MaxBodyBytes),
		requestLogMiddleware(),
		httpGin.Recovery(),
	}
	if cfg.PromReg != nil {
		chain = append(chain, newHTTPMetricsMiddleware(cfg.PromReg))
	}
	// Rate limiting must run before authentication, otherwise the 401/403
	// responses abort the chain and leave token brute force unthrottled.
	// Pre-authentication the user id is unknown, so clients are keyed by
	// source address.
	if cfg.RateLimit != nil {
		chain = append(chain, newRateLimitMiddleware(
			rate.Limit(cfg.RateLimit.RequestsPerSecond),
			cfg.RateLimit.Burst,
		))
	}
	if cfg.RequireAuth || len(cfg.AuthUsers) > 0 {
		authService := NewAuthService(cfg.AuthUsers)
		publicPaths := append(
			[]string{"/healthz", "/readyz", "/metrics", "/version"},
			cfg.PublicPaths...,
		)
		adminPaths := append(
			[]string{"/debug/pprof", "/debug/pprof/**"},
			cfg.AdminPaths...,
		)
		chain = append(
			chain,
			wrapHandler(NewAuthMiddleware(authService, publicPaths, adminPaths)),
		)
	}
	return chain
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
				http.StatusTooManyRequests,
				v1.ErrorCodeRateLimited,
				"too many requests",
			)
			c.Abort()
			return
		}
		c.Next()
	}
}
