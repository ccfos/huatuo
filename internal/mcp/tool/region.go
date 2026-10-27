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

package tool

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/ccfos/huatuo/internal/mcp/utils"
)

type regionValue[T any] struct {
	name  string
	value T
}

type compiledRegion[T any] struct {
	pattern *regexp.Regexp
	value   T
}

type regionRouter[T any] struct {
	fallback T
	exact    map[string]T
	ordered  []compiledRegion[T]
}

func newRegionRouter[T any](fallback T, regions []regionValue[T]) (*regionRouter[T], error) {
	if len(regions) > 0 {
		fallback = regions[0].value
	}
	router := &regionRouter[T]{
		fallback: fallback,
		exact:    make(map[string]T, len(regions)),
		ordered:  make([]compiledRegion[T], 0, len(regions)),
	}
	for _, region := range regions {
		if region.name == "" {
			return nil, fmt.Errorf("region name is required")
		}
		if _, exists := router.exact[region.name]; exists {
			return nil, fmt.Errorf("duplicate region name %q", region.name)
		}
		pattern, err := regexp.Compile(region.name)
		if err != nil {
			return nil, fmt.Errorf("compile region pattern %q: %w", region.name, err)
		}
		router.exact[region.name] = region.value
		router.ordered = append(router.ordered, compiledRegion[T]{
			pattern: pattern,
			value:   region.value,
		})
	}
	return router, nil
}

func (r *regionRouter[T]) forRegion(region string) T {
	if value, exists := r.exact[region]; exists {
		return value
	}
	for _, candidate := range r.ordered {
		if candidate.pattern.MatchString(region) {
			return candidate.value
		}
	}
	return r.fallback
}

type regionOutcome[T any] struct {
	Region string
	Value  T
	Err    error
}

type backendRegionContextKey struct{}

func requiredRegions(request mcpgo.CallToolRequest, maximum int) ([]string, error) {
	return utils.RequiredRegions(request.GetArguments(), maximum)
}

func fanOutRegions[T any](
	ctx context.Context,
	regions []string,
	maximumConcurrent int,
	query func(context.Context, string) (T, error),
) []regionOutcome[T] {
	if maximumConcurrent <= 0 {
		maximumConcurrent = 1
	}
	semaphore := make(chan struct{}, min(maximumConcurrent, len(regions)))
	outcomes := make([]regionOutcome[T], len(regions))
	var wait sync.WaitGroup
	for index, region := range regions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			outcomes[index].Region = region
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				outcomes[index].Err = ctx.Err()
				return
			}
			regionCtx := context.WithValue(ctx, backendRegionContextKey{}, region)
			outcomes[index].Value, outcomes[index].Err = query(regionCtx, region)
		}()
	}
	wait.Wait()
	return outcomes
}
