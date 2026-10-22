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
	"fmt"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/auth"
	"github.com/ccfos/huatuo/internal/mcp/constant"

	"golang.org/x/time/rate"
)

// ErrRateLimitExceeded indicates that an attempt exceeded its request budget.
var ErrRateLimitExceeded = errors.New("MCP request rate limit exceeded")

// RateLimitError exposes rejection scope without requiring string parsing or
// embedding backend identity in a budget shared by multiple adapters.
type RateLimitError struct {
	Scope       string
	PrincipalID string
}

func (e *RateLimitError) Error() string {
	if e.Scope == constant.RateLimitScopeUser {
		return fmt.Sprintf("%s for principal %q", ErrRateLimitExceeded, e.PrincipalID)
	}
	return ErrRateLimitExceeded.Error() + " for backend"
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimitExceeded }

// RateLimiter applies backend and authenticated-principal request budgets.
type RateLimiter struct {
	backend  *rate.Limiter
	users    *PrincipalRateBudgets
	observer func(string)
}

// PrincipalRateBudgets shares per-principal limits across MCP backends.
type PrincipalRateBudgets struct {
	rpm   int
	mu    sync.Mutex
	users map[string]*rate.Limiter
}

// NewRateLimiter creates request budgets; non-positive RPM disables a scope.
func NewRateLimiter(backendRPM, userRPM int) *RateLimiter {
	return NewRateLimiterWithUsers(backendRPM, NewPrincipalRateBudgets(userRPM))
}

// NewPrincipalRateBudgets creates shared authenticated-principal budgets.
func NewPrincipalRateBudgets(rpm int) *PrincipalRateBudgets {
	return &PrincipalRateBudgets{rpm: rpm, users: make(map[string]*rate.Limiter)}
}

// NewRateLimiterWithUsers creates a backend budget sharing principal limits.
func NewRateLimiterWithUsers(backendRPM int, users *PrincipalRateBudgets) *RateLimiter {
	return &RateLimiter{
		backend: newRPMTokenBucket(backendRPM),
		users:   users,
	}
}

// AllowBackendRequest charges one attempt to both configured rate budgets.
func (l *RateLimiter) AllowBackendRequest(ctx context.Context) error {
	if l == nil {
		return nil
	}
	now := time.Now()
	backendReservation, ok := reserveImmediateRateToken(l.backend, now)
	if !ok {
		l.notifyRateLimitRejection(constant.RateLimitScopeGlobal)
		return &RateLimitError{Scope: constant.RateLimitScopeGlobal}
	}
	principal, ok := auth.PrincipalFromContext(ctx)
	if ok && principal.ID != "" {
		if _, allowed := l.users.reservePrincipalRateToken(principal.ID, now); !allowed {
			if backendReservation != nil {
				backendReservation.CancelAt(now)
			}
			l.notifyRateLimitRejection(constant.RateLimitScopeUser)
			return &RateLimitError{Scope: constant.RateLimitScopeUser, PrincipalID: principal.ID}
		}
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

func (u *PrincipalRateBudgets) reservePrincipalRateToken(id string, now time.Time) (*rate.Reservation, bool) {
	if u == nil || u.rpm <= 0 {
		return nil, true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.users[id] == nil {
		u.users[id] = newRPMTokenBucket(u.rpm)
	}
	return reserveImmediateRateToken(u.users[id], now)
}

func reserveImmediateRateToken(limiter *rate.Limiter, now time.Time) (*rate.Reservation, bool) {
	if limiter == nil {
		return nil, true
	}
	reservation := limiter.ReserveN(now, 1)
	if !reservation.OK() || reservation.DelayFrom(now) > 0 {
		reservation.CancelAt(now)
		return nil, false
	}
	return reservation, true
}

func newRPMTokenBucket(rpm int) *rate.Limiter {
	if rpm <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Every(constant.RateLimitWindow/time.Duration(rpm)), rpm)
}
