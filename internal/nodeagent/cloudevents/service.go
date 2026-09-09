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

// Package cloudevents manages filtered Node event subscriptions and converts
// tracing documents into the public CloudEvents envelope.
package cloudevents

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"huatuo-bamai/internal/matcher"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	"huatuo-bamai/pkg/types"
)

var (
	// ErrInvalidFilters indicates that an event filter cannot be compiled.
	ErrInvalidFilters = errors.New("cloud events: invalid filters")
	// ErrLimitExceeded indicates that the subscription capacity is exhausted.
	ErrLimitExceeded = errors.New("cloud events: subscription limit exceeded")
)

// Filters contains optional regular expressions applied to event fields.
type Filters struct {
	TracerName             string
	Hostname               string
	ContainerHostname      string
	ContainerHostNamespace string
	ContainerQoS           string
	Region                 string
}

// Service owns event subscription admission and filtering.
type Service struct {
	store            *tracingstore.Store
	maxSubscriptions int
	active           atomic.Int32
}

// New constructs a CloudEvents subscription service.
func New(store *tracingstore.Store, maxSubscriptions int) (*Service, error) {
	if store == nil {
		return nil, errors.New("create cloud events service: tracing store is required")
	}
	if maxSubscriptions <= 0 {
		return nil, errors.New("create cloud events service: maximum subscriptions must be positive")
	}
	return &Service{store: store, maxSubscriptions: maxSubscriptions}, nil
}

// Subscription delivers matching CloudEvents until its context is canceled or
// Close is called.
type Subscription struct {
	events    <-chan types.WatchEvent
	cancel    context.CancelFunc
	done      <-chan struct{}
	closeOnce sync.Once
}

// Events returns the subscription's event channel.
func (s *Subscription) Events() <-chan types.WatchEvent {
	return s.events
}

// Close stops the subscription and waits for its resources to be released.
func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
	})
}

// Subscribe opens a bounded subscription for matching event documents.
func (s *Service) Subscribe(ctx context.Context, filters *Filters) (*Subscription, error) {
	fieldMatcher, err := newMatcher(filters)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidFilters, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.tryAcquire() {
		return nil, ErrLimitExceeded
	}

	documents, unsubscribe := s.store.Subscribe()
	subscriptionCtx, cancel := context.WithCancel(ctx)
	events := make(chan types.WatchEvent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		defer s.release()
		defer unsubscribe()

		for {
			select {
			case <-subscriptionCtx.Done():
				return
			case document, ok := <-documents:
				if !ok {
					return
				}
				if document.TracerRunType != types.TracerRunTypeEvent || !fieldMatcher.Match(document) {
					continue
				}
				event := documentToWatchEvent(document)
				select {
				case events <- event:
				case <-subscriptionCtx.Done():
					return
				}
			}
		}
	}()

	return &Subscription{
		events: events,
		cancel: cancel,
		done:   done,
	}, nil
}

func (s *Service) tryAcquire() bool {
	for {
		active := s.active.Load()
		if int(active) >= s.maxSubscriptions {
			return false
		}
		if s.active.CompareAndSwap(active, active+1) {
			return true
		}
	}
}

func (s *Service) release() {
	s.active.Add(-1)
}

func newMatcher(filters *Filters) (*matcher.FieldMatcher[*tracingstore.Document], error) {
	if filters == nil {
		filters = &Filters{}
	}
	return matcher.NewFieldMatcher([]matcher.FieldSpec[*tracingstore.Document]{
		{
			Name:    "tracer_name",
			Pattern: filters.TracerName,
			Extract: func(document *tracingstore.Document) string { return document.TracerName },
		},
		{
			Name:    "hostname",
			Pattern: filters.Hostname,
			Extract: func(document *tracingstore.Document) string { return document.Hostname },
		},
		{
			Name:    "container_hostname",
			Pattern: filters.ContainerHostname,
			Extract: func(document *tracingstore.Document) string { return document.ContainerHostname },
		},
		{
			Name:    "container_host_namespace",
			Pattern: filters.ContainerHostNamespace,
			Extract: func(document *tracingstore.Document) string { return document.ContainerHostNamespace },
		},
		{
			Name:    "container_qos",
			Pattern: filters.ContainerQoS,
			Extract: func(document *tracingstore.Document) string { return document.ContainerQoS },
		},
		{
			Name:    "region",
			Pattern: filters.Region,
			Extract: func(document *tracingstore.Document) string { return document.Region },
		},
	})
}
