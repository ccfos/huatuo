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

package bpf

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReferencePublish(t *testing.T) {
	var reference Reference

	require.Error(t, reference.Publish(nil))
	require.NoError(t, reference.Publish(&defaultBPF{}))
	require.Error(t, reference.Publish(&defaultBPF{}))
	require.NoError(t, reference.UnPublish())
}

func TestReferenceAcquire(t *testing.T) {
	var reference Reference
	_, ok := reference.Acquire()
	require.False(t, ok)

	object := &defaultBPF{}
	require.NoError(t, reference.Publish(object))

	lease, ok := reference.Acquire()
	require.True(t, ok)
	require.Same(t, object, lease.BPF)

	lease.Release()
	lease.Release()
	require.NoError(t, reference.UnPublish())
}

func TestReferenceUnPublishWaitsForLease(t *testing.T) {
	var reference Reference
	object := &defaultBPF{}
	require.NoError(t, reference.Publish(object))

	lease, ok := reference.Acquire()
	require.True(t, ok)

	unpublishDone := make(chan error, 1)
	go func() {
		unpublishDone <- reference.UnPublish()
	}()

	deadline := time.Now().Add(time.Second)
	for {
		reference.mu.Lock()
		isUnpublished := reference.object == nil
		reference.mu.Unlock()
		if isUnpublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("UnPublish() did not remove the object")
		}
		runtime.Gosched()
	}

	_, ok = reference.Acquire()
	require.False(t, ok)
	select {
	case err := <-unpublishDone:
		t.Fatalf("UnPublish() returned while the object was leased: %v", err)
	default:
	}

	lease.Release()
	select {
	case err := <-unpublishDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("UnPublish() did not return after Release()")
	}

	loaded, err := object.Loaded()
	require.NoError(t, err)
	require.False(t, loaded)
}
