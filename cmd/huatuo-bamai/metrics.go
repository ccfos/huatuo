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
	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/metric/runtime"

	"github.com/prometheus/client_golang/prometheus"
)

func (d *Daemon) setupMetrics() error {
	reg, err := initMetricsCollector(config.Get().BlackList, d.opts.Region)
	if err != nil {
		return err
	}
	d.metrics = reg

	return nil
}

func initMetricsCollector(blackListed []string, region string) (*prometheus.Registry, error) {
	nc, err := metric.NewCollectorManager(blackListed, region)
	if err != nil {
		return nil, err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(nc)

	runtime.RegisterCollector(reg, metric.DefaultNamespace)
	return reg, nil
}
