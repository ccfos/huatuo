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
	"container/list"
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
	limiters := newClientLimiters(r, burst)
	return func(c *httpGin.Context) {
		key := internalContext(c).UserID
		if key == "" {
			key = c.Request.RemoteAddr
			if host, _, err := net.SplitHostPort(key); err == nil {
				key = host
			}
		}
		if !limiters.allow(key, time.Now()) {
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

const (
	// limiterPruneInterval controls how often idle client limiters are
	// pruned; pruning walks the LRU list from the oldest side only.
	limiterPruneInterval = 1000
	// limiterIdleTTL is how long an unused limiter is kept.
	limiterIdleTTL = 10 * time.Minute
	// maxLimiters caps tracked clients so source-address churn cannot grow
	// the map without bound.
	maxLimiters = 10000
)

type limiterEntry struct {
	key      string
	limiter  *rate.Limiter
	lastSeen time.Time
}

// clientLimiters tracks per-client rate limiters. Entries are LRU-ordered:
// when the map is full the least recently seen client is evicted, and idle
// entries are pruned from the oldest side every limiterPruneInterval
// requests.
type clientLimiters struct {
	mu       sync.Mutex
	limit    rate.Limit
	burst    int
	entries  map[string]*list.Element
	order    *list.List // front = most recently seen
	requests uint64
}

func newClientLimiters(limit rate.Limit, burst int) *clientLimiters {
	return &clientLimiters{
		limit:   limit,
		burst:   burst,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

func (c *clientLimiters) allow(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requests++
	if c.requests%limiterPruneInterval == 0 {
		c.pruneLocked(now)
	}

	if el, ok := c.entries[key]; ok {
		entry := el.Value.(*limiterEntry)
		entry.lastSeen = now
		c.order.MoveToFront(el)
		return entry.limiter.Allow()
	}

	entry := &limiterEntry{
		key:      key,
		limiter:  rate.NewLimiter(c.limit, c.burst),
		lastSeen: now,
	}
	c.entries[key] = c.order.PushFront(entry)
	if len(c.entries) > maxLimiters {
		c.evictOldestLocked()
	}

	return entry.limiter.Allow()
}

func (c *clientLimiters) evictOldestLocked() {
	el := c.order.Back()
	if el == nil {
		return
	}
	entry := c.order.Remove(el).(*limiterEntry)
	delete(c.entries, entry.key)
}

func (c *clientLimiters) pruneLocked(now time.Time) {
	for c.order.Len() > 0 {
		entry := c.order.Back().Value.(*limiterEntry)
		if now.Sub(entry.lastSeen) <= limiterIdleTTL {
			return
		}
		c.order.Remove(c.order.Back())
		delete(c.entries, entry.key)
	}
}
