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
	"context"
	"time"

	"huatuo-bamai/internal/log"
)

const ioHealthMDRetryInterval = 10 * time.Second

func (c *ioHealthCollector) superviseMDWatcher(
	ctx context.Context,
	worker ioHealthEvidenceSubmitter,
	retry <-chan time.Time,
) {
	for {
		watcher := c.newMDWatcher(c.procMDStatPath, c.sysBlockPath)
		if err := watcher.Start(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warnf("io_health: start MD watcher: %v; will retry", err)
			if !waitIOHealthRetry(ctx, retry) {
				return
			}
			continue
		}

		err := c.consumeMDWatcher(ctx, watcher, worker)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			log.Warnf("io_health: MD watcher stopped; will retry")
		} else {
			log.Warnf("io_health: MD watcher failed: %v; will retry", err)
		}
		if !waitIOHealthRetry(ctx, retry) {
			return
		}
	}
}

func (c *ioHealthCollector) consumeMDWatcher(
	ctx context.Context,
	watcher ioHealthMDWatcher,
	worker ioHealthEvidenceSubmitter,
) error {
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- watcher.Wait()
	}()

	changes := watcher.Changes()
	for {
		select {
		case <-ctx.Done():
			<-waitResult
			return ctx.Err()
		case change, ok := <-changes:
			if ok {
				c.handleMDChange(change, worker)
			} else {
				changes = nil
			}
		case err := <-waitResult:
			return err
		}
	}
}

func waitIOHealthRetry(ctx context.Context, retry <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-retry:
		return true
	}
}
