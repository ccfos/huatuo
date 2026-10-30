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

// Package utils provides shared MCP parsing and resource-control helpers.
package utils

import (
	"context"
	"errors"
	"time"

	"golang.org/x/time/rate"
)

const (
	RateLimitScopeGlobal = "global"
	RateLimitWindow      = time.Minute
)

// ErrRateLimitExceeded indicates that an attempt exceeded its request budget.
var ErrRateLimitExceeded = errors.New("MCP request rate limit exceeded")

// RateLimitError exposes rejection scope without requiring string parsing or
// embedding backend identity in a budget shared by multiple adapters.
type RateLimitError struct {
	Scope string
}

func (e *RateLimitError) Error() string {
	return ErrRateLimitExceeded.Error() + " for backend"
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimitExceeded }

// RateLimiter bounds backend attempts, including retries. User request limits
// are enforced by the outer API server, not by MCP.
type RateLimiter struct {
	backend  *rate.Limiter
	observer func(string)
}

// NewRateLimiter creates a backend budget; non-positive RPM disables it.
func NewRateLimiter(backendRPM int) *RateLimiter {
	return &RateLimiter{
		backend: newRPMTokenBucket(backendRPM),
	}
}

// AllowBackendRequest charges each network attempt to its backend budget.
func (l *RateLimiter) AllowBackendRequest(_ context.Context) error {
	if l == nil {
		return nil
	}
	if l.backend != nil && !l.backend.Allow() {
		l.notifyRateLimitRejection(RateLimitScopeGlobal)
		return &RateLimitError{Scope: RateLimitScopeGlobal}
	}
	return nil
}

// SetRateLimitObserver installs an initialization-time rejection callback.
func (l *RateLimiter) SetRateLimitObserver(observer func(string)) {
	if l != nil {
		l.observer = observer
	}
}

func (l *RateLimiter) notifyRateLimitRejection(scope string) {
	if l.observer != nil {
		l.observer(scope)
	}
}

func newRPMTokenBucket(rpm int) *rate.Limiter {
	if rpm <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Every(RateLimitWindow/time.Duration(rpm)), rpm)
}
