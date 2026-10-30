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

package main

import (
	"context"
	"fmt"

	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/metric/runtime"

	"github.com/prometheus/client_golang/prometheus"
)

func setupMetrics(d *Daemon) (func(context.Context) error, error) {
	collectors := make(map[string]metric.Collector)
	for name, registration := range d.registrations {
		if registration.Flag&tracing.FlagMetric == 0 {
			continue
		}

		collector, ok := registration.TracingData.(metric.Collector)
		if !ok {
			return nil, fmt.Errorf("%w: %q does not implement metric.Collector", tracing.ErrInvalidTracer, name)
		}

		collectors[name] = collector
	}

	nc, err := metric.NewCollectorManager(collectors, d.opts.Region)
	if err != nil {
		return nil, err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(nc)

	runtime.RegisterCollector(reg, metric.DefaultNamespace)
	d.metrics = reg

	return nil, nil
}
