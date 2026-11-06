// Copyright 2025, 2026 The HuaTuo Authors
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

package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEventTracerNamesPersistedMatchRegistration pins the canonical tracer
// names. Each name is authored in three independent places that must agree:
//  1. the RegisterEventTracing key (registration),
//  2. the tracing document's tracer_name field (what tracing.Save persists),
//  3. the tracer_name filters in the shipped Grafana dashboard
//     (build/docker/grafana/dashboards/autotracing-event.json).
//
// (1) and (2) share the constants below, so they cannot drift in code; this
// test guards their value against the documented event names
// (docs/best-practice/events-watch_en.md). A rename here breaks the dashboard
// queries and silently empties the corresponding panels.
func TestEventTracerNamesPersistedMatchRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		actual   string
		expected string
	}{
		{
			name:     "netdev events",
			actual:   netdevEventsTracerName,
			expected: "netdev_events",
		},
		{
			name:     "netdev bonding lacp",
			actual:   lacpTracerName,
			expected: "netdev_bonding_lacp",
		},
		{
			name:     "memory reclaim events",
			actual:   memoryReclaimTracerName,
			expected: "memory_reclaim_events",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.actual)
		})
	}
}
