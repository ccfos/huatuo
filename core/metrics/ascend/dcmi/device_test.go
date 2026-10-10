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

package dcmi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDcGetDeviceNetWorkHealthReturnsContextCancellation(t *testing.T) {
	original := dcGetDeviceNetWorkHealth
	block := make(chan struct{})
	entered := make(chan struct{})
	dcGetDeviceNetWorkHealth = func(uint32, uint32, *uint32) Return {
		close(entered)
		<-block
		return Success
	}
	t.Cleanup(func() {
		close(block)
		dcGetDeviceNetWorkHealth = original
	})

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := libdcmi.DcGetDeviceNetWorkHealth(ctx, 0, 0)
		result <- err
	}()

	<-entered
	start := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DcGetDeviceNetWorkHealth() error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed >= time.Second {
			t.Fatalf("DcGetDeviceNetWorkHealth() returned after %v, want before the DCMI timeout", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("DcGetDeviceNetWorkHealth() did not return after context cancellation")
	}
}
