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

package tracing

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"
)

const (
	FlagMetric uint32 = 1 << iota
	FlagTracing
)

type EventTracingAttr struct {
	Interval    int
	Flag        uint32
	TracingData any
}

const (
	statusDisabled  = "disabled"
	statusActive    = "active"
	statusInactive  = "inactive"
	statusInitError = "initError"
)

var (
	factories             = make(map[string]func() (*EventTracingAttr, error))
	tracingEventAttrCache = make(map[string]*EventTracingAttr)
	tracingStatusCache    = make(map[string]string)
	registrationOnce      sync.Once
	errRegistration       error
	registrationBlacklist []string
)

func RegisterEventTracing(name string, factory func() (*EventTracingAttr, error)) {
	factories[name] = factory
}

func NewRegister(blacklist []string) (map[string]*EventTracingAttr, error) {
	normalizedBlacklist := slices.Clone(blacklist)
	slices.Sort(normalizedBlacklist)
	normalizedBlacklist = slices.Compact(normalizedBlacklist)

	registrationOnce.Do(func() {
		registrationBlacklist = normalizedBlacklist
		registrations := make(map[string]*EventTracingAttr)
		for name, factory := range factories {
			if slices.Contains(normalizedBlacklist, name) {
				tracingStatusCache[name] = statusDisabled
				continue
			}

			if factory == nil {
				tracingStatusCache[name] = statusInitError
				errRegistration = fmt.Errorf("%w: %q factory is nil", ErrInvalidTracer, name)
				return
			}

			attr, err := factory()
			if err != nil {
				if errors.Is(err, types.ErrNotSupported) {
					tracingStatusCache[name] = statusInactive
					continue
				}

				tracingStatusCache[name] = statusInitError
				errRegistration = fmt.Errorf("initialize tracer %q: %w", name, err)
				return
			}
			if attr == nil {
				tracingStatusCache[name] = statusInitError
				errRegistration = fmt.Errorf("%w: %q factory returned nil", ErrInvalidTracer, name)
				return
			}
			if attr.Flag&(FlagTracing|FlagMetric) == 0 {
				tracingStatusCache[name] = statusInitError
				errRegistration = fmt.Errorf("%w: %q has no role", ErrInvalidTracer, name)
				return
			}

			tracingStatusCache[name] = statusActive
			registrations[name] = attr

			log.WithField("tracer", name).Info("tracer registered")
		}

		tracingEventAttrCache = registrations
	})

	if errRegistration != nil {
		return nil, errRegistration
	}
	if !slices.Equal(normalizedBlacklist, registrationBlacklist) {
		return nil, fmt.Errorf(
			"%w: blacklist differs from the initialized registry",
			ErrInvalidTracer,
		)
	}

	return cloneEventTracingAttrs(tracingEventAttrCache), nil
}

func EventTracingStatus() map[string]string {
	return maps.Clone(tracingStatusCache)
}

func cloneEventTracingAttrs(attrs map[string]*EventTracingAttr) map[string]*EventTracingAttr {
	cloned := make(map[string]*EventTracingAttr, len(attrs))
	for name, attr := range attrs {
		attrCopy := *attr
		cloned[name] = &attrCopy
	}

	return cloned
}
