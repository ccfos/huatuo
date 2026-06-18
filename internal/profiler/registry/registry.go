// Copyright 2025, 2026 The HuaTuo Authors
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

package registry

import (
	"context"
	"fmt"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
)

// Per-language fallback to an implementation key, consulted by Get when no
// exact (lang, type) is registered.
var defaultLangProfiler = map[string]string{
	"go":  "native",
	"c":   "native",
	"c++": "native",
}

// ProfilerMeta groups identity fields (used as registry keys) and the
// Profiler/Aggregator behavior pair driven by Profile.
type ProfilerMeta struct {
	Type        string
	LangOrImpl  string
	Description string

	Impl          Profiler
	NewAggregator func(*pcontext.ProfilerContext) aggregator.Aggregator
}

// Profiler is the sampling lifecycle. Aggregator ownership stays with Profile
// so streaming (eBPF, async-profiler tail) and one-shot (py-spy) providers
// share one shape; ReadDataLoop returns when ctx is cancelled or sampling
// completes naturally, and its error surfaces through Profile.
type Profiler interface {
	Start(pctx *pcontext.ProfilerContext) error
	ReadDataLoop(ctx context.Context, addRecord func(any)) error
	Stop(pctx *pcontext.ProfilerContext) error
}

// Registration is init-time only; profilerRegistry is read without locking
// after init() ordering finishes. Do not call Register from request paths.
var profilerRegistry = map[string]map[string]ProfilerMeta{}

// Register adds meta to the registry keyed by (LangOrImpl, Type). Duplicate
// (lang, type) pairs panic — registration is init-time and a duplicate almost
// always indicates two providers fighting over the same slot.
func Register(meta ProfilerMeta) {
	if profilerRegistry[meta.LangOrImpl] == nil {
		profilerRegistry[meta.LangOrImpl] = make(map[string]ProfilerMeta)
	}

	if _, dup := profilerRegistry[meta.LangOrImpl][meta.Type]; dup {
		panic(fmt.Sprintf("registry: duplicate profiler %s/%s", meta.LangOrImpl, meta.Type))
	}

	profilerRegistry[meta.LangOrImpl][meta.Type] = meta
}

// Get resolves (lang, typ). If no exact match exists, the language is replaced
// by its default implementation key — e.g. ("go", "cpu") falls back to
// ("native", "cpu") — and the lookup is retried.
func Get(langOrImpl, typ string) (ProfilerMeta, error) {
	if meta, ok := profilerRegistry[langOrImpl][typ]; ok {
		return meta, nil
	}

	if impl, ok := defaultLangProfiler[langOrImpl]; ok {
		if meta, ok := profilerRegistry[impl][typ]; ok {
			return meta, nil
		}
	}

	return ProfilerMeta{}, fmt.Errorf("no profiler for lang=%s type=%s", langOrImpl, typ)
}

// Profile blocks until cleanup completes so resources are guaranteed released.
// ReadDataLoop returning on its own is a legitimate stop reason — one-shot
// samplers (py-spy) finish before the duration timer fires.
func Profile(pctx *pcontext.ProfilerContext, p ProfilerMeta) error {
	aggr := p.NewAggregator(pctx)
	pipe := aggregator.NewPipeline(pctx, aggr)
	pipe.Start()
	log.P().Info("aggregator started")

	if err := p.Impl.Start(pctx); err != nil {
		pipe.Stop()
		pctx.Cancel()

		return fmt.Errorf("start profiler: %w", err)
	}

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- p.Impl.ReadDataLoop(pctx.Ctx, pipe.Enqueue)
	}()

	var deadline <-chan time.Time

	if pctx.Duration > 0 {
		t := time.NewTimer(time.Duration(pctx.Duration) * time.Second)
		defer t.Stop()

		deadline = t.C
	}

	var (
		err     error
		loopErr error
		looped  bool
	)

	select {
	case <-deadline:
		log.P().Info("profiler auto-stop by duration")
	case <-pctx.Ctx.Done():
		err = pctx.Ctx.Err()
		log.P().Infof("profiler stop by context: %v", err)
	case loopErr = <-loopDone:
		looped = true
		log.P().Infof("profiler stop by loop exit: %v", loopErr)
	}

	// Cancel first so ReadDataLoop exits before Stop closes BPF/file handles
	// the loop may still be reading from.
	pctx.Cancel()

	if !looped {
		loopErr = <-loopDone
	}

	if stopErr := p.Impl.Stop(pctx); stopErr != nil {
		log.P().Errorf("profiler stop: %v", stopErr)
	}

	pipe.Stop()

	if err == nil && loopErr != nil {
		err = loopErr
	}

	return err
}
