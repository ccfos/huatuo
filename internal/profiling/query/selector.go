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

package query

import (
	"fmt"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"

	"github.com/ccfos/huatuo/pkg/observation"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
)

type profileSelector struct {
	documentFilter profilingstore.Filter
	sampleMatchers []*labels.Matcher
}

func isDocumentLabel(name string) bool {
	if observation.IsMetadataField(name) {
		return true
	}

	switch name {
	case "id", "scope", "__profile_type__", "profile_type", "tracer_name", "tracer_id", "tracer_type":
		return true
	default:
		return false
	}
}

func parseProfileSelector(expression, profileType string) (*profileSelector, error) {
	matchers, err := parser.ParseMetricSelector(expression)
	if err != nil {
		return nil, fmt.Errorf("%w: parse selector: %w", ErrInvalidQuery, err)
	}

	selector := &profileSelector{}
	var documentMatchers []*labels.Matcher
	for _, matcher := range matchers {
		if isDocumentLabel(matcher.Name) {
			if matcher.Name == "__profile_type__" && profileType == "" {
				profileType = matcher.Value
			}
			documentMatchers = append(documentMatchers, matcher)
			continue
		}

		if strings.HasPrefix(matcher.Name, "__") {
			return nil, fmt.Errorf("%w: unsupported reserved label %q", ErrInvalidQuery, matcher.Name)
		}

		if matcher.Type != labels.MatchEqual && matcher.Type != labels.MatchRegexp {
			return nil, fmt.Errorf("%w: sample label %q only supports = and =~", ErrInvalidQuery, matcher.Name)
		}

		selector.sampleMatchers = append(selector.sampleMatchers, matcher)
	}

	selector.documentFilter.ProfileType = profileType
	if err := resolveProfileMatchers(&selector.documentFilter, documentMatchers, profileType); err != nil {
		return nil, err
	}

	return selector, nil
}
