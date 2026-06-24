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

package driver

import (
	"context"
	"errors"
	"testing"
)

type testBackend struct{}

func (b *testBackend) Init(context.Context, string, []Index) error {
	return nil
}

func (b *testBackend) Save(context.Context, Record) error {
	return nil
}

func (b *testBackend) Get(context.Context, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (b *testBackend) Delete(context.Context, string) error {
	return nil
}

func (b *testBackend) Query(context.Context, Query) ([]Record, error) {
	return nil, nil
}

func (b *testBackend) Count(context.Context, Query) (int64, error) {
	return 0, nil
}

func (b *testBackend) Values(context.Context, string, Query, int) ([]string, error) {
	return nil, nil
}

func (b *testBackend) Close(context.Context) error { return nil }

func cloneBackendFactories() map[string]BackendFactory {
	backendFactoriesMu.RLock()
	defer backendFactoriesMu.RUnlock()

	cloned := make(map[string]BackendFactory, len(backendFactories))
	for name, factory := range backendFactories {
		cloned[name] = factory
	}
	return cloned
}

func restoreBackendFactories(snapshot map[string]BackendFactory) {
	backendFactoriesMu.Lock()
	defer backendFactoriesMu.Unlock()

	backendFactories = make(map[string]BackendFactory, len(snapshot))
	for name, factory := range snapshot {
		backendFactories[name] = factory
	}
}

// TestRegisterBackend covers driver registry registration: verifies the factory is stored in the global registry and can be retrieved by driver name.
func TestRegisterBackend(t *testing.T) {
	snapshot := cloneBackendFactories()
	defer restoreBackendFactories(snapshot)

	backendFactoriesMu.Lock()
	backendFactories = make(map[string]BackendFactory)
	backendFactoriesMu.Unlock()

	RegisterBackend("memory", func(*Config) (Backend, error) {
		return &testBackend{}, nil
	})

	backendFactoriesMu.RLock()
	factory, ok := backendFactories["memory"]
	backendFactoriesMu.RUnlock()

	if !ok {
		t.Errorf("backendFactories[\"memory\"] not found")
	}
	if factory == nil {
		t.Errorf("backendFactories[\"memory\"] factory is nil")
	}
}

// TestNewBackend covers backend creation from config: verifies empty driver, unregistered driver, nil factory, successful creation from a registered driver, and factory error.
func TestNewBackend(t *testing.T) {
	snapshot := cloneBackendFactories()
	defer restoreBackendFactories(snapshot)

	backendFactoriesMu.Lock()
	backendFactories = make(map[string]BackendFactory)
	backendFactoriesMu.Unlock()

	backendCreateErr := errors.New("backend create failed")

	RegisterBackend("memory", func(cfg *Config) (Backend, error) {
		if cfg.SQLiteDSN == "return-error" {
			return nil, backendCreateErr
		}
		return &testBackend{}, nil
	})
	RegisterBackend("nil_factory", nil)

	cases := []struct {
		name     string
		config   Config
		validate func(*testing.T, Backend, error)
	}{
		{
			name:   "empty driver",
			config: Config{},
			validate: func(t *testing.T, backend Backend, err error) {
				if backend != nil {
					t.Errorf("NewBackend() backend = %#v, want nil", backend)
				}
				if err == nil {
					t.Errorf("NewBackend() error = nil, want error")
				}
			},
		},
		{
			name: "driver not registered",
			config: Config{
				Driver: "sqlite",
			},
			validate: func(t *testing.T, backend Backend, err error) {
				if backend != nil {
					t.Errorf("NewBackend() backend = %#v, want nil", backend)
				}
				if err == nil {
					t.Errorf("NewBackend() error = nil, want error")
				}
			},
		},
		{
			name: "nil factory",
			config: Config{
				Driver: "nil_factory",
			},
			validate: func(t *testing.T, backend Backend, err error) {
				if backend != nil {
					t.Errorf("NewBackend() backend = %#v, want nil", backend)
				}
				if err == nil {
					t.Errorf("NewBackend() error = nil, want error")
				}
			},
		},
		{
			name: "driver registered",
			config: Config{
				Driver:    "memory",
				SQLiteDSN: "memory://jobs",
			},
			validate: func(t *testing.T, backend Backend, err error) {
				if err != nil {
					t.Errorf("NewBackend() returned error: %v", err)
				}
				if backend == nil {
					t.Errorf("NewBackend() backend is nil")
				}
			},
		},
		{
			name: "factory returns error",
			config: Config{
				Driver:    "memory",
				SQLiteDSN: "return-error",
			},
			validate: func(t *testing.T, backend Backend, err error) {
				if backend != nil {
					t.Errorf("NewBackend() backend = %#v, want nil", backend)
				}
				if !errors.Is(err, backendCreateErr) {
					t.Errorf("NewBackend() error = %v, want %v", err, backendCreateErr)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := NewBackend(&tc.config)
			tc.validate(t, backend, err)
		})
	}
}
