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
	"fmt"
	"slices"
	"sync"
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
	factories           = make(map[string]func() (*EventTracingAttr, error))
	tracingEventAttrMap = make(map[string]*EventTracingAttr)
	tracingOnce         sync.Once
)

func RegisterEventTracing(name string, factory func() (*EventTracingAttr, error)) {
	factories[name] = factory
}

func NewRegister(blackListed []string) (map[string]*EventTracingAttr, error) {
	var err error

	tracingOnce.Do(func() {
		tracingMap := make(map[string]*EventTracingAttr)
		var attr *EventTracingAttr

		for key, factory := range factories {
			if slices.Contains(blackListed, key) {
				continue
			}

			attr, err = factory()
			if err != nil {
				return
			}
			if attr.Flag&(FlagTracing|FlagMetric) == 0 {
				err = fmt.Errorf("invalid flag")
				return
			}
			tracingMap[key] = attr
		}
		tracingEventAttrMap = tracingMap
	})

	if err != nil {
		return nil, err
	}

	return tracingEventAttrMap, nil
}
