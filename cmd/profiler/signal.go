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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func newSignalContext(parent context.Context, logBuf *bytes.Buffer) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case sig := <-signals:
			if sig == syscall.SIGINT {
				cancel(profile.ErrStopRequested)
			} else {
				cancel(context.Canceled)
			}
			fmt.Fprintf(logBuf, "[signal] caught signal: %s, canceling context\n", sig)
		}
	}()
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			signal.Stop(signals)
			cancel(nil)
			// The caller may read logBuf as soon as cleanup returns.
			<-done
		})
	}
	return ctx, cleanup
}
