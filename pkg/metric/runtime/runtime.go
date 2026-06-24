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

// Package runtime registers Go runtime and process-level Prometheus
// collectors (memory, GC, file descriptors, CPU time, etc.) under the
// HuaTuo metrics namespace.
package runtime

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// RegisterCollector registers the standard process and Go runtime
// collectors with reg, prefixing their metric names with namespace so
// they share a consistent prefix with the rest of the HuaTuo metrics.
func RegisterCollector(reg *prometheus.Registry, namespace string) {
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{
		Namespace: namespace,
	}))

	prefixedReg := prometheus.WrapRegistererWithPrefix(namespace+"_", reg)
	prefixedReg.MustRegister(collectors.NewGoCollector())
}
