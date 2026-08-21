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

package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestDaemonRunCleansUpInReverseOrder(t *testing.T) {
	var calls []string
	daemon := &Daemon{
		steps: []daemonStep{
			newTestStep("first", &calls),
			newTestStep("second", &calls),
			newTestStep("third", &calls),
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := daemon.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	want := []string{
		"setup first",
		"setup second",
		"setup third",
		"cleanup third",
		"cleanup second",
		"cleanup first",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDaemonRunRollsBackInitializedSteps(t *testing.T) {
	var calls []string
	daemon := &Daemon{
		steps: []daemonStep{
			newTestStep("first", &calls),
			newTestStep("second", &calls),
			{
				name: "failed",
				setup: func(context.Context, *Daemon) (func(context.Context) error, error) {
					calls = append(calls, "setup failed")
					return nil, errors.New("setup failed")
				},
			},
		},
	}

	err := daemon.Run(t.Context())
	if err == nil || err.Error() != "failed: setup failed" {
		t.Fatalf("Run error = %v, want %q", err, "failed: setup failed")
	}

	want := []string{
		"setup first",
		"setup second",
		"setup failed",
		"cleanup second",
		"cleanup first",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSetupMetricsObservesAgentRequests(t *testing.T) {
	daemon := &Daemon{}
	if _, err := setupMetrics(t.Context(), daemon); err != nil {
		t.Fatalf("setupMetrics() error = %v", err)
	}
	daemon.agentObserver("start", time.Second, nil)

	families, err := daemon.metrics.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	want := map[string]bool{
		"huatuo_apiserver_agent_requests_total":           false,
		"huatuo_apiserver_agent_request_duration_seconds": false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; ok {
			want[family.GetName()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %q was not gathered", name)
		}
	}
}

func newTestStep(name string, calls *[]string) daemonStep {
	return daemonStep{
		name: name,
		setup: func(context.Context, *Daemon) (func(context.Context) error, error) {
			*calls = append(*calls, "setup "+name)
			return func(context.Context) error {
				*calls = append(*calls, "cleanup "+name)
				return nil
			}, nil
		},
	}
}
