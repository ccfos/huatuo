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

package executil

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunDuringExitAndCancellation(t *testing.T) {
	for _, name := range []string{"exit first", "cancel first", "concurrent"} {
		t.Run(name, func(t *testing.T) {
			spec, options, ready, release := newGatedCommand(t, 0)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan struct{})
			var runErr error
			go func() {
				_, runErr = Run(ctx, spec, options...)
				close(finished)
			}()
			t.Cleanup(func() {
				cancel()
				release()
				<-finished
			})
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("command did not become ready")
			}

			switch name {
			case "exit first":
				release()
			case "cancel first":
				cancel()
			case "concurrent":
				barrier := make(chan struct{})
				var group sync.WaitGroup
				for _, action := range []func(){release, cancel} {
					group.Add(1)
					go func() {
						defer group.Done()
						<-barrier
						action()
					}()
				}
				close(barrier)
				group.Wait()
			}

			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not finish after exit or cancellation")
			}
			if name == "exit first" {
				cancel()
				if runErr != nil {
					t.Fatalf("Run(): %v", runErr)
				}
			} else if runErr != nil || name == "cancel first" {
				if !errors.Is(runErr, context.Canceled) || !errors.Is(runErr, ErrRunCanceled) {
					t.Fatalf("Run(): %v, want pure cancellation", runErr)
				}
			}
		})
	}
}
