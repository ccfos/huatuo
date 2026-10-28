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

// Package runtime registers process, Go runtime, and application information metrics.
package runtime

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/ccfos/huatuo/pkg/metric"
)

// InfoMetric describes an information gauge with a constant value of 1.
type InfoMetric struct {
	Name   string
	Help   string
	Labels map[string]string
}

// RegisterCollector registers process and Go runtime collectors, plus the
// supplied information gauges fixed at 1.
// All metrics use the namespace prefix and common host/region labels.
// Labels are copied during registration; host and region are reserved.
// Invalid descriptors or duplicate registration panic, as with MustRegister.
func RegisterCollector(reg *prometheus.Registry, namespace string, infos ...InfoMetric) {
	prefix := ""
	if namespace != "" {
		prefix = namespace + "_"
	}

	labeledReg := prometheus.WrapRegistererWith(prometheus.Labels{
		metric.LabelHost:   metric.DefaultHostname(),
		metric.LabelRegion: metric.DefaultRegion(),
	}, prometheus.WrapRegistererWithPrefix(prefix, reg))

	labeledReg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	labeledReg.MustRegister(collectors.NewGoCollector())

	for _, info := range infos {
		labeledReg.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name:        info.Name,
				Help:        info.Help,
				ConstLabels: prometheus.Labels(info.Labels),
			},
			func() float64 { return 1 },
		))
	}
}
