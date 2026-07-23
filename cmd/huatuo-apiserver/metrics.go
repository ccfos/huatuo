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

package main

import (
	"context"
	"strconv"
	"time"

	"huatuo-bamai/internal/job"
	"huatuo-bamai/pkg/metric/runtime"

	"github.com/prometheus/client_golang/prometheus"
)

const promNamespace = "huatuo_apiserver"

func setupMetrics(_ context.Context, d *Daemon) (func(context.Context) error, error) {
	registry := prometheus.NewRegistry()
	runtime.RegisterCollector(registry, promNamespace)
	agentRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Subsystem: "agent",
		Name:      "requests_total",
		Help:      "Total Agent HTTP requests.",
	}, []string{"operation", "success"})
	agentDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: promNamespace,
		Subsystem: "agent",
		Name:      "request_duration_seconds",
		Help:      "Agent HTTP request latency.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"operation"})
	registry.MustRegister(agentRequests, agentDuration)
	d.agentObserver = func(operation string, duration time.Duration, err error) {
		agentRequests.WithLabelValues(operation, strconv.FormatBool(err == nil)).Inc()
		agentDuration.WithLabelValues(operation).Observe(duration.Seconds())
	}
	d.metrics = registry

	return nil, nil
}

type jobManagerCollector struct {
	manager             *job.Manager
	active              *prometheus.Desc
	quotaRejections     *prometheus.Desc
	persistenceFailures *prometheus.Desc
	recoveredJobs       *prometheus.Desc
	shutdownIncomplete  *prometheus.Desc
}

func newJobManagerCollector(manager *job.Manager) *jobManagerCollector {
	return &jobManagerCollector{
		manager: manager,
		active: prometheus.NewDesc(
			promNamespace+"_jobs_active",
			"Active jobs by type and status.",
			[]string{"type", "status"}, nil,
		),
		quotaRejections: prometheus.NewDesc(
			promNamespace+"_job_quota_rejections_total",
			"Total jobs rejected by quota limits.", nil, nil,
		),
		persistenceFailures: prometheus.NewDesc(
			promNamespace+"_job_persistence_failures_total",
			"Total job persistence failures.", nil, nil,
		),
		recoveredJobs: prometheus.NewDesc(
			promNamespace+"_jobs_recovered_total",
			"Total active jobs recovered at startup.", nil, nil,
		),
		shutdownIncomplete: prometheus.NewDesc(
			promNamespace+"_job_shutdown_incomplete_total",
			"Total job shutdowns that exceeded their deadline.", nil, nil,
		),
	}
}

func (c *jobManagerCollector) Describe(out chan<- *prometheus.Desc) {
	out <- c.active
	out <- c.quotaRejections
	out <- c.persistenceFailures
	out <- c.recoveredJobs
	out <- c.shutdownIncomplete
}

func (c *jobManagerCollector) Collect(out chan<- prometheus.Metric) {
	stats := c.manager.Stats()
	for _, active := range stats.Active {
		out <- prometheus.MustNewConstMetric(
			c.active, prometheus.GaugeValue, float64(active.Count), string(active.Type), string(active.Status),
		)
	}
	out <- prometheus.MustNewConstMetric(c.quotaRejections, prometheus.CounterValue, float64(stats.QuotaRejections))
	out <- prometheus.MustNewConstMetric(c.persistenceFailures, prometheus.CounterValue, float64(stats.PersistenceFailures))
	out <- prometheus.MustNewConstMetric(c.recoveredJobs, prometheus.CounterValue, float64(stats.RecoveredJobs))
	out <- prometheus.MustNewConstMetric(c.shutdownIncomplete, prometheus.CounterValue, float64(stats.ShutdownIncomplete))
}
