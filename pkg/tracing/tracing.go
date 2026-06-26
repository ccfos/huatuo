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

// Package tracing is the core scheduling and lifecycle layer for HuaTuo
// tracers and event sources.
//
// It defines the ITracingEvent and ITracingMetric interfaces, manages
// per-tracer goroutines through EventTracing, and exposes a global
// manager for registering, starting, and stopping tracing tasks.
// The package also provides the document model and watch-event publish/
// subscribe mechanism used to deliver tracer output to consumers.
package tracing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"
)

// EventTracing represents a tracing
type EventTracing struct {
	ic        ITracingEvent
	name      string
	interval  int
	hitCount  atomic.Int32
	mu        sync.Mutex
	cancelCtx context.CancelFunc
	stopCh    chan struct{}
	stopOnce  sync.Once
	exit      atomic.Bool
	isRunning atomic.Bool
	flag      uint32
}

// ITracingEvent represents a tracing/event
type ITracingEvent interface {
	Start(ctx context.Context) error
}

// NewTracingEvent create a new tracing
func NewTracingEvent(tracing *EventTracingAttr, name string) *EventTracing {
	return &EventTracing{
		ic:       tracing.TracingData.(ITracingEvent),
		name:     name,
		interval: tracing.Interval,
		flag:     tracing.Flag,
	}
}

// Start do work
func (c *EventTracing) Start() error {
	c.isRunning.Store(true)
	c.exit.Store(false)
	c.stopCh = make(chan struct{})
	c.stopOnce = sync.Once{}

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancelCtx = cancel
	c.mu.Unlock()

	go func() {
		defer c.isRunning.Store(false)
		for !c.exit.Load() {
			c.doStart(ctx)

			c.hitCount.Add(1)

			if c.exit.Load() {
				break
			}

			timer := time.NewTimer(time.Duration(c.interval) * time.Second)
			select {
			case <-timer.C:
			case <-c.stopCh:
			}
			timer.Stop()
		}

		log.Infof("tracing exited: %s", c.name)
	}()

	log.Infof("start tracing %s", c.name)
	return nil
}

func (c *EventTracing) doStart(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("tracing %s panicked: %v", c.name, r)
		}
	}()

	if err := c.ic.Start(ctx); err != nil {
		if !(errors.Is(err, types.ErrExitByCancelCtx) ||
			errors.Is(err, types.ErrDisconnectedHuatuo) ||
			errors.Is(err, types.ErrNotSupported)) {
			log.Errorf("start tracing %s: %v", c.name, err)
		}

		if errors.Is(err, types.ErrNotSupported) {
			c.exit.Store(true)
		}
	}
}

// Stop stop tracing
func (c *EventTracing) Stop() {
	c.exit.Store(true)
	c.stopOnce.Do(func() {
		if c.stopCh != nil {
			close(c.stopCh)
		}
	})
	c.mu.Lock()
	if c.cancelCtx != nil {
		c.cancelCtx()
	}
	c.mu.Unlock()
}

// EventTracingInfo represents tracing information
type EventTracingInfo struct {
	Name     string `json:"name"`
	Running  bool   `json:"running"`
	HitCount int    `json:"hit"`
	Interval int    `json:"restart_interval"`
	Flag     uint32 `json:"flag"`
}

// Info return tracing's base information
func (c *EventTracing) Info() *EventTracingInfo {
	return &EventTracingInfo{
		Name:     c.name,
		Running:  c.isRunning.Load(),
		HitCount: int(c.hitCount.Load()),
		Interval: c.interval,
		Flag:     c.flag,
	}
}
