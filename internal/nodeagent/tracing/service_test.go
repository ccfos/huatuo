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

package tracing

import (
	"context"
	"errors"
	"testing"
	"time"

	"huatuo-bamai/internal/nodeagent/operation"
	"huatuo-bamai/pkg/observation"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

func newOperationManager(t *testing.T) *operation.Manager {
	t.Helper()
	manager, err := operation.NewManager(operation.Config{
		MaxConcurrent: 1,
		Lifecycle: operation.LifecyclePolicy{
			LaunchTimeout:           time.Second,
			StopGracePeriod:         time.Second,
			FinalizationTimeout:     time.Second,
			TerminalRetentionPeriod: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("operation.NewManager() error = %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	return manager
}

func TestStartValidatesThenReportsExecutorUnavailable(t *testing.T) {
	service, err := NewService(newOperationManager(t))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	request := StartRequest{
		RequestID: "job-1",
		Duration:  time.Minute,
		Scope:     observation.ScopeHost,
		Spec: tracingdomain.Spec{
			Type: tracingdomain.TypeNetworkingDrop,
		},
	}
	operationSnapshot, created, err := service.Start(t.Context(), &request)
	if !errors.Is(err, ErrNotImplemented) || created || operationSnapshot != nil {
		t.Fatalf("Start() = (%+v, %t, %v)", operationSnapshot, created, err)
	}
}

func TestStartRejectsUnsupportedScopeBeforeAvailability(t *testing.T) {
	service, err := NewService(newOperationManager(t))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	_, _, err = service.Start(t.Context(), &StartRequest{
		RequestID:   "job-1",
		Duration:    time.Minute,
		Scope:       observation.ScopeContainer,
		ContainerID: "container-1",
		Spec: tracingdomain.Spec{
			Type: tracingdomain.TypeNetworkingDrop,
		},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() error = %v, want ErrInvalidRequest", err)
	}
}

func TestStartRejectsNilRequest(t *testing.T) {
	service, err := NewService(newOperationManager(t))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, _, err := service.Start(t.Context(), nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() error = %v, want ErrInvalidRequest", err)
	}
}
