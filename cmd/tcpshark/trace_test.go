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
	"errors"
	"testing"
)

func TestAllErrorGroupJoinsFirstWorkerAndShutdownErrors(t *testing.T) {
	rateLimitErr := errors.New("rate-limit reader failed")
	finalDrainErr := errors.New("final drain failed")
	group, groupCtx := newAllErrorGroup(t.Context())
	streamStarted := make(chan struct{})

	group.Go(func() error {
		<-streamStarted
		return rateLimitErr
	})
	group.Go(func() error {
		close(streamStarted)
		<-groupCtx.Done()
		return finalDrainErr
	})

	err := group.Wait()
	if !errors.Is(err, rateLimitErr) {
		t.Errorf("allErrorGroup.Wait() error = %v, want rate-limit error", err)
	}
	if !errors.Is(err, finalDrainErr) {
		t.Errorf("allErrorGroup.Wait() error = %v, want final-drain error", err)
	}
}
