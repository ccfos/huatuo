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

package services

import (
	"fmt"
	"net"
	"net/http"
	"syscall"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/services/config"
	"huatuo-bamai/pkg/tracing"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
)

var instance *Server

// Server http server instance
type Server struct {
	server       *gin.Engine
	mgrTracing   *tracing.MgrTracingEvent
	promRegistry *prometheus.Registry
}

// RateLimiter define a rate limiter
type RateLimiter struct {
	limiter *rate.Limiter
}

// NewRateLimiter create a new RateLimiter
func NewRateLimiter(r rate.Limit, b int) *RateLimiter {
	return &RateLimiter{
		limiter: rate.NewLimiter(r, b),
	}
}

// Limit do limit work
func (rl *RateLimiter) Limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rl.limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many requests",
			})
			return
		}
		c.Next()
	}
}

// NewServer new http instance
func NewServer() *Server {
	gin.SetMode(gin.ReleaseMode)

	instance = &Server{
		server: gin.New(),
	}

	// middleware: log, recovery, pprof
	pprof.Register(instance.server)
	limiter := NewRateLimiter(200, 200)
	instance.server.Use(gin.Logger(), gin.Recovery(), limiter.Limit())
	return instance
}

// AddHandler add a new handler
func (s *Server) AddHandler(method, path string, handlerFunc gin.HandlerFunc) {
	s.server.Handle(method, path, handlerFunc)
}

// Run tcp server
func (s *Server) Run(addr string) error {
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

	// for tracing 'net_rx_latency' keep the skb timestamp enabled,
	// kernel func net_enable_timestamp() is system wide, can enable by set SOF_TIMESTAMPING_RX_SOFTWARE,
	// ref: https://www.kernel.org/doc/html/latest/networking/timestamping.html.
	flags := unix.SOF_TIMESTAMPING_RX_SOFTWARE
	if err := syscall.SetsockoptInt(int(file.Fd()), syscall.SOL_SOCKET, syscall.SO_TIMESTAMPING, flags); err != nil {
		return fmt.Errorf("set sockopt %w", err)
	}

	if err := s.server.RunListener(tcpListener); err != nil {
		log.Errorf("Server error: %s", err.Error())
		return err
	}
	return nil
}

// Start start API service
func Start(addr string, mgrTracing *tracing.MgrTracingEvent, promRegistry *prometheus.Registry) {
	s := NewServer()
	s.mgrTracing = mgrTracing
	s.promRegistry = promRegistry

	s.AddHandler("POST", "/config", config.Config)
	s.AddHandler("GET", "/metrics", CollectorDo)
	s.AddHandler("POST", "/task/start", NewTask)
	s.AddHandler("GET", "/task/result", TaskResult)
	s.AddHandler("POST", "/task/stop", TaskStop)
	s.AddHandler("GET", "/containers/json", ContainersList)
	s.AddHandler("GET", "/tracing/json", TracingList)

	// will be removed
	s.AddHandler("GET", "/tracer", TracerList)
	s.AddHandler("POST", "/tracer/start", TracerStart)
	s.AddHandler("POST", "/tracer/stop", TracerStop)
	s.AddHandler("POST", "/tracer/stop_all", TracerStopAll)

	go func() {
		if err := s.Run(addr); err != nil {
			log.Errorf("start tcp api server: %v", err)
		}
	}()
}
