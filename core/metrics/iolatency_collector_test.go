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

	"huatuo-bamai/internal/bpf"
)

type iolatencyBPFStub struct {
	bpf.BPF
	containerErr error
}

func (s *iolatencyBPFStub) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name == blkContainerLatencyMap {
		return nil, s.containerErr
	}

	return nil, nil
}

func (s *iolatencyBPFStub) Close() error {
	return nil
}

func TestIOLatencyUpdateReturnsContainerCollectionError(t *testing.T) {
	expectedErr := errors.New("container latency map unavailable")
	collector := &iolatencyTracing{}
	if err := collector.bpfObject.Publish(&iolatencyBPFStub{
		containerErr: expectedErr,
	}); err != nil {
		t.Fatalf("publish BPF stub: %v", err)
	}
	t.Cleanup(func() {
		if err := collector.bpfObject.UnPublish(); err != nil {
			t.Errorf("unpublish BPF stub: %v", err)
		}
	})

	metrics, err := collector.Update()
	if !errors.Is(err, expectedErr) {
		t.Fatalf("Update() error = %v, want %v", err, expectedErr)
	}
	if len(metrics) != 0 {
		t.Fatalf("Update() returned %d metrics, want 0", len(metrics))
	}
}
