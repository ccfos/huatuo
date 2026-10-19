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
	"fmt"
	"runtime/debug"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	goprovider "github.com/ccfos/huatuo/internal/memsnapshot/providers/golang"
	"github.com/ccfos/huatuo/internal/memsnapshot/providers/java"
	"github.com/ccfos/huatuo/internal/memsnapshot/providers/python"
)

// provider returns a non-nil complete, partial or unavailable snapshot with no error.
// Failures and context cancellation return an error with no snapshot. The
// collector owns failed snapshots so process memory survives runtime failures.
type provider interface {
	Snapshot(ctx context.Context, request memsnapshot.Request) (*memsnapshot.Snapshot, error)
}

func newProvider(language memsnapshot.Language) provider {
	switch language {
	case memsnapshot.LanguageGo:
		return goprovider.New()
	case memsnapshot.LanguageJava:
		return java.New()
	case memsnapshot.LanguagePython:
		return python.New()
	default:
		return nil
	}
}

// Normalize failures before recording duration so replacement snapshots retain
// the provider-stage elapsed time. The caller handles parent cancellation.
func snapshotProvider(ctx context.Context, p provider,
	process memsnapshot.ProcessInstanceID, maxMemoryObjectEntries int,
) (snapshot *memsnapshot.Snapshot) {
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.WithField("pid", process.TGID).
				WithField("panic", recovered).
				WithField("stack", string(debug.Stack())).
				Error("runtime snapshot panicked")
			snapshot = memsnapshot.Failed(fmt.Sprintf("runtime snapshot panic: %v", recovered))
		} else if err := ctx.Err(); err != nil {
			snapshot = memsnapshot.Failed("snapshot process runtime: " + err.Error())
		}
		snapshot.DurationMS = uint64((time.Since(started) + time.Millisecond - 1) / time.Millisecond)
	}()

	if p == nil {
		return memsnapshot.Unavailable("runtime is not supported")
	}

	request := memsnapshot.Request{
		Process:                process,
		MaxMemoryObjectEntries: maxMemoryObjectEntries,
		SamplingSeed:           uint64(started.UnixNano()),
	}
	snapshot, err := p.Snapshot(ctx, request)
	if err != nil {
		return memsnapshot.Failed(err.Error())
	}
	return snapshot
}
