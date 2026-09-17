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
	"fmt"
	"sync"
)

// BackendFactory creates a backend from Config.
type BackendFactory func(*Config) (Backend, error)

var (
	backendFactoriesMu sync.RWMutex
	backendFactories   = make(map[string]BackendFactory)
)

// RegisterBackend registers a backend factory by driver name.
func RegisterBackend(name string, factory BackendFactory) {
	backendFactoriesMu.Lock()
	defer backendFactoriesMu.Unlock()
	backendFactories[name] = factory
}

// NewBackend creates a backend from Config.
func NewBackend(cfg *Config) (Backend, error) {
	if cfg == nil {
		return nil, fmt.Errorf("storage: backend config is nil")
	}
	if cfg.Driver == "" {
		return nil, fmt.Errorf("storage: backend driver is empty")
	}

	backendFactoriesMu.RLock()
	factory, ok := backendFactories[cfg.Driver]
	backendFactoriesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("storage: backend driver %q not registered", cfg.Driver)
	}
	if factory == nil {
		return nil, fmt.Errorf("storage: backend driver %q has nil factory", cfg.Driver)
	}

	return factory(cfg)
}
