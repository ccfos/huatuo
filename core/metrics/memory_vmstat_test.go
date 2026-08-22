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

package collector

import (
	"errors"
	"testing"

	"huatuo-bamai/pkg/metric"
)

func TestCollectMemoryVmstatReturnsPartialMetricsWithHostError(t *testing.T) {
	wantMetric := &metric.Data{Value: 1}
	wantErr := errors.New("read host vmstat")

	metrics, err := collectMemoryVmstat(
		func() ([]*metric.Data, error) {
			return []*metric.Data{wantMetric}, nil
		},
		func() ([]*metric.Data, error) {
			return nil, wantErr
		},
	)

	if !errors.Is(err, wantErr) {
		t.Fatalf("collectMemoryVmstat() error = %v, want %v", err, wantErr)
	}
	if len(metrics) != 1 || metrics[0] != wantMetric {
		t.Fatalf("collectMemoryVmstat() metrics = %v, want partial container metrics", metrics)
	}
}

func TestCollectMemoryVmstatStopsAfterContainerError(t *testing.T) {
	wantErr := errors.New("read container vmstat")
	hostCalled := false

	metrics, err := collectMemoryVmstat(
		func() ([]*metric.Data, error) {
			return nil, wantErr
		},
		func() ([]*metric.Data, error) {
			hostCalled = true
			return nil, nil
		},
	)

	if !errors.Is(err, wantErr) {
		t.Fatalf("collectMemoryVmstat() error = %v, want %v", err, wantErr)
	}
	if metrics != nil {
		t.Fatalf("collectMemoryVmstat() metrics = %v, want nil", metrics)
	}
	if hostCalled {
		t.Fatal("collectMemoryVmstat() called host collector after container error")
	}
}
