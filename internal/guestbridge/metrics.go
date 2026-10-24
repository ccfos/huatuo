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

package guestbridge

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

type bridgeMetrics struct {
	listenerUp     *prometheus.GaugeVec
	connections    prometheus.Gauge
	received       prometheus.Counter
	dropped        *prometheus.CounterVec
	receiveErrors  *prometheus.CounterVec
	ingestDuration prometheus.Histogram
}

func newBridgeMetrics(registerer prometheus.Registerer) (*bridgeMetrics, error) {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	metrics := &bridgeMetrics{
		listenerUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "huatuo_bamai",
			Subsystem: "guestbridge",
			Name:      "listener_up",
			Help:      "Whether a GuestBridge listener manager is running.",
		}, []string{"transport"}),
		connections: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "huatuo_bamai",
			Subsystem: "guestbridge",
			Name:      "connections",
			Help:      "Current accepted GuestBridge connections.",
		}),
		received: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "huatuo_bamai",
			Subsystem: "guestbridge",
			Name:      "documents_received_total",
			Help:      "Valid Guest documents ingested by the Host.",
		}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "huatuo_bamai",
			Subsystem: "guestbridge",
			Name:      "documents_dropped_total",
			Help:      "Guest documents dropped during business validation.",
		}, []string{"reason"}),
		receiveErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "huatuo_bamai",
			Subsystem: "guestbridge",
			Name:      "receive_errors_total",
			Help:      "Guest connection protocol and frame errors.",
		}, []string{"reason"}),
		ingestDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "huatuo_bamai",
			Subsystem: "guestbridge",
			Name:      "document_ingest_duration_seconds",
			Help:      "Time spent validating and ingesting Guest documents.",
		}),
	}

	collectors := []prometheus.Collector{
		metrics.listenerUp,
		metrics.connections,
		metrics.received,
		metrics.dropped,
		metrics.receiveErrors,
		metrics.ingestDuration,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("guestbridge: register metrics: %w", err)
		}
	}
	return metrics, nil
}
