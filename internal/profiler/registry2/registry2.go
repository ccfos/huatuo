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

package registry2

import (
	"context"
	"fmt"

	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	registryv2 "huatuo-bamai/internal/profiler/registry/v2"
)

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

// Profiler is the legacy one-shot interface kept for compatibility.
type Profiler interface {
	StartProfile(pctx *ProfilerContext) error
}

var profilerRegistry = map[string]map[string]ProfilerMeta{} // language -> type -> ProfilerMeta

type legacyProfilerAdapter struct {
	legacy Profiler
}

func (a *legacyProfilerAdapter) Start(pctx *pcontext.ProfilerContext) error {
	err := a.legacy.StartProfile(pctx)
	// Legacy profilers run to completion in StartProfile; cancel so registry/v2
	// does not wait for an additional full duration window.
	if pctx.Cancel != nil {
		pctx.Cancel()
	}
	return err
}

func (a *legacyProfilerAdapter) ReadDataLoop(ctx context.Context, addRecord func(any)) {}

func (a *legacyProfilerAdapter) Stop(pctx *pcontext.ProfilerContext, aggr *aggregator.Aggregator) error {
	return nil
}

func (a *legacyProfilerAdapter) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	// Legacy profiler handles aggregation internally.
	return aggregator.NewAggregator(
		pctx,
		func(any) {},
		func(*pcontext.ProfilerContext) (any, error) { return nil, nil },
	)
}

func RegisterProfilerMeta(langOrImpl, typ string, meta ProfilerMeta) {
	if profilerRegistry[langOrImpl] == nil {
		profilerRegistry[langOrImpl] = make(map[string]ProfilerMeta)
	}
	profilerRegistry[langOrImpl][typ] = meta

	registryv2.RegisterProfilerMeta(langOrImpl, typ, registryv2.ProfilerMeta{
		Type:        meta.Type,
		LangOrImpl:  meta.LangOrImpl,
		Description: meta.Description,
		Impl:        &legacyProfilerAdapter{legacy: meta.Impl},
	})
}

// GetProfiler resolves a legacy profiler by language and type, falling back to
// the language's default profiler (e.g. "go" -> "native") if no exact match is
// registered.
func GetProfiler(langOrImpl, typ string) (ProfilerMeta, error) {
	if m, ok := profilerRegistry[langOrImpl]; ok {
		if meta, ok := m[typ]; ok {
			return meta, nil
		}
	}

	if profiler, ok := defaultLangProfiler[langOrImpl]; ok {
		if m, ok := profilerRegistry[profiler]; ok {
			if meta, ok := m[typ]; ok {
				return meta, nil
			}
		}
	}

	return ProfilerMeta{}, fmt.Errorf("no profiler for lang=%s type=%s", langOrImpl, typ)
}

// Profile keeps the old direct-call behavior.
func Profile(ctx *ProfilerContext, p ProfilerMeta) error {
	err := p.Impl.StartProfile(ctx)
	if err != nil {
		return fmt.Errorf("start profile error: %w", err)
	}
	return nil
}
