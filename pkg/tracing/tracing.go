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

package tracing

import (
	"context"
	"errors"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"
)

// EventTracing represents a tracing
type EventTracing struct {
	ic        ITracingEvent
	name      string
	interval  int
	hitCount  int
	cancelCtx context.CancelFunc
	exit      bool
	isRunning bool
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
	c.isRunning = true
	c.exit = false

	go func() {
		for !c.exit {
			c.doStart()

			c.hitCount++

			if c.exit {
				break
			}

			time.Sleep(time.Duration(c.interval) * time.Second)
		}

		c.isRunning = false
		log.Infof("%s: tracing goroutine exited", c.name)
	}()

	log.Infof("start tracing %s", c.name)
	return nil
}

func (c *EventTracing) doStart() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancelCtx = cancel
	defer c.cancelCtx()

	if err := c.ic.Start(ctx); err != nil {
		if !(errors.Is(err, types.ErrExitByCancelCtx) ||
			errors.Is(err, types.ErrDisconnectedHuatuo) ||
			errors.Is(err, types.ErrNotSupported)) {
			log.Errorf("start tracing %s: %v", c.name, err)
		}

		if errors.Is(err, types.ErrNotSupported) {
			c.exit = true
		}
	}
}

// Stop stop tracing
func (c *EventTracing) Stop() {
	c.exit = true
	c.cancelCtx()
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
		Running:  c.isRunning,
		HitCount: c.hitCount,
		Interval: c.interval,
		Flag:     c.flag,
	}
}
