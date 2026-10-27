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

package utils

import (
	"context"
	"errors"
	"testing"

	"github.com/ccfos/huatuo/internal/httpserver/auth"
)

func TestBackendBudgetsAreIndependent(t *testing.T) {
	es := NewRateLimiter(1)
	prometheus := NewRateLimiter(1)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "operator"})

	if err := es.AllowBackendRequest(ctx); err != nil {
		t.Fatalf("first ES attempt: %v", err)
	}
	if err := es.AllowBackendRequest(ctx); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("second ES attempt error = %v, want ErrExceeded", err)
	}
	if err := prometheus.AllowBackendRequest(ctx); err != nil {
		t.Fatalf("same user's independent Prometheus budget: %v", err)
	}
}

func TestBackendBudgetDoesNotRequirePrincipal(t *testing.T) {
	limiter := NewRateLimiter(1)
	if err := limiter.AllowBackendRequest(context.Background()); err != nil {
		t.Fatalf("first backend attempt: %v", err)
	}
	if err := limiter.AllowBackendRequest(context.Background()); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("second backend attempt error = %v, want ErrExceeded", err)
	} else {
		var rejection *RateLimitError
		if !errors.As(err, &rejection) || rejection.Scope != "global" {
			t.Fatalf("missing global rejection details: %v", err)
		}
	}
}

func TestBackendBudgetIsSharedAcrossUsers(t *testing.T) {
	limiter := NewRateLimiter(1)
	first := auth.WithPrincipal(context.Background(), auth.Principal{ID: "first"})
	second := auth.WithPrincipal(context.Background(), auth.Principal{ID: "second"})
	if err := limiter.AllowBackendRequest(first); err != nil {
		t.Fatalf("first backend attempt: %v", err)
	}
	if err := limiter.AllowBackendRequest(second); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("exhausted backend error = %v, want ErrExceeded", err)
	}
	disabled := NewRateLimiter(0)
	for range 3 {
		if err := disabled.AllowBackendRequest(first); err != nil {
			t.Fatalf("disabled backend limit rejected user: %v", err)
		}
	}
}

func TestLimiterReportsRejectionScope(t *testing.T) {
	limiter := NewRateLimiter(1)
	var scopes []string
	limiter.SetRateLimitObserver(func(scope string) { scopes = append(scopes, scope) })
	if err := limiter.AllowBackendRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := limiter.AllowBackendRequest(t.Context()); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("Allow() error = %v", err)
	}
	if len(scopes) != 1 || scopes[0] != "global" {
		t.Fatalf("rejection scopes = %v", scopes)
	}
}
