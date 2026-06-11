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

package registry

import (
	"context"
	"fmt"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
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

type Profiler interface {
	Start(pctx *pcontext.ProfilerContext) error
	ReadDataLoop(ctx context.Context, addRecord func(any))
	Stop(pctx *pcontext.ProfilerContext, aggregator *aggregator.Aggregator) error
	NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator
}

var profilerRegistry = map[string]map[string]ProfilerMeta{}

func RegisterProfilerMeta(langOrImpl, typ string, meta ProfilerMeta) {
	if profilerRegistry[langOrImpl] == nil {
		profilerRegistry[langOrImpl] = make(map[string]ProfilerMeta)
	}
	profilerRegistry[langOrImpl][typ] = meta
}

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

func Profile(pctx *pcontext.ProfilerContext, p ProfilerMeta) error {
	aggr := p.Impl.NewAggregator(pctx)

	aggr.Start()
	log.P().Infof("Aggregator started successfully")

	if err := p.Impl.Start(pctx); err != nil {
		aggr.Stop()
		pctx.Cancel()
		return fmt.Errorf("failed to start profiler: %w", err)
	}

	go p.Impl.ReadDataLoop(pctx.Ctx, aggr.AddRecord)

	if pctx.Duration > 0 {
		timer := time.NewTimer(time.Duration(pctx.Duration) * time.Second)
		defer timer.Stop()

		select {
		case <-timer.C:
			log.P().Infof("profiler auto-stop by duration")
		case <-pctx.Ctx.Done():
			log.P().Infof("profiler stop by context")
		}

		if err := p.Impl.Stop(pctx, aggr); err != nil {
			log.P().Infof("profiler stop error: %v", err)
		}
	}

	return nil
}
