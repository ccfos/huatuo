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

package tracing

import (
	"errors"
	"fmt"
	"slices"
	"sync"

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

var (
	factories             = make(map[string]func() (*EventTracingAttr, error))
	tracingEventAttrCache = make(map[string]*EventTracingAttr)
	tracingOnceCache      sync.Once
)

func RegisterEventTracing(name string, factory func() (*EventTracingAttr, error)) {
	factories[name] = factory
}

func NewRegister(blackListed []string) (map[string]*EventTracingAttr, error) {
	var err error

	tracingOnceCache.Do(func() {
		tracingMap := make(map[string]*EventTracingAttr)
		var attr *EventTracingAttr

		for name, factory := range factories {
			if slices.Contains(blackListed, name) {
				continue
			}

			attr, err = factory()
			if err != nil {
				if errors.Is(err, types.ErrNotSupported) {
					err = nil // reset the error if it is the last error in the loop.
					continue
				}

				err = fmt.Errorf("traing name: %s, err: [%w]", name, err)
				return
			}
			if attr.Flag&(FlagTracing|FlagMetric) == 0 {
				err = fmt.Errorf("traing name: %s, invalid flag", name)
				return
			}
			tracingMap[name] = attr
		}
		tracingEventAttrCache = tracingMap
	})

	if err != nil {
		return nil, err
	}

	return tracingEventAttrCache, nil
}
