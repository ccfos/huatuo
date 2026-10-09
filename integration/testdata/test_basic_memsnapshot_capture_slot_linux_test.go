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

//go:build integration && linux

package collector

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestCaptureSlotAcceptance(t *testing.T) {
	identity, err := memsnapshot.ReadProcessInstanceID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	captureSlot <- struct{}{}
	held := true
	defer func() {
		if held {
			<-captureSlot
		}
	}()
	if result, err := Snapshot(t.Context(), identity, Options{}); result != nil || !errors.Is(err, ErrCaptureBusy) {
		t.Fatalf("busy result=%+v error=%v", result, err)
	}
	if result, err := Snapshot(t.Context(), identity, Options{WaitForCapture: true, SnapshotTimeout: time.Millisecond}); result != nil || !errors.Is(err, ErrCaptureBusy) {
		t.Fatalf("wait budget result=%+v error=%v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := Snapshot(ctx, identity, Options{WaitForCapture: true, SnapshotTimeout: time.Minute})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}
	// A waiting request cannot acquire the held slot, but must recover after release.
	waiting := make(chan error, 1)
	go func() {
		waiting <- acquireCapture(t.Context(), Options{WaitForCapture: true, SnapshotTimeout: time.Second})
	}()
	select {
	case err := <-waiting:
		t.Fatalf("waiter returned before slot release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	<-captureSlot
	held = false
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire released slot")
	}
	<-captureSlot
	if err := acquireCapture(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	<-captureSlot
}
