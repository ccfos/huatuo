// Copyright 2025 The HuaTuo Authors
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
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
)

// defaultLangProfiler maps a language to the implementation key used as a
// fallback when no exact (lang, type) profiler is registered.
var defaultLangProfiler = map[string]string{
	"go":  "native",
	"c":   "native",
	"c++": "native",
}

type ProfilerMeta struct {
	Type        string
	LangOrImpl  string
	Description string
	Impl        Profiler
}

// Profiler defines the lifecycle of a sampling implementation.
type Profiler interface {
	Start(pctx *pcontext.ProfilerContext) error
	ReadDataLoop(ctx context.Context, addRecord func(any))
	Stop(pctx *pcontext.ProfilerContext, aggregator *aggregator.Aggregator) error
	NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator
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

// Profile runs the full sampling lifecycle: aggregator start, impl start,
// async data drain, then stop on duration timeout or context cancellation.
// It blocks until cleanup completes so resources are guaranteed released.
func Profile(pctx *pcontext.ProfilerContext, p ProfilerMeta) error {
	agg := p.Impl.NewAggregator(pctx)
	agg.Start()
	log.P().Infof("aggregator started")

	if err := p.Impl.Start(pctx); err != nil {
		agg.Stop()
		pctx.Cancel()

		return fmt.Errorf("start profiler: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Impl.ReadDataLoop(pctx.Ctx, agg.AddRecord)
	}()

	var deadline <-chan time.Time
	if pctx.Duration > 0 {
		t := time.NewTimer(time.Duration(pctx.Duration) * time.Second)
		defer t.Stop()

		deadline = t.C
	}

	select {
	case <-deadline:
		log.P().Infof("profiler auto-stop by duration")
	case <-pctx.Ctx.Done():
		log.P().Infof("profiler stop by context")
	}

	// Cancel first so ReadDataLoop exits before Stop closes BPF/file handles
	// the loop may still be reading from.
	pctx.Cancel()
	wg.Wait()

	if err := p.Impl.Stop(pctx, agg); err != nil {
		log.P().Errorf("profiler stop: %v", err)
	}

	return nil
}
