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

package metric

import (
	"errors"
	"sort"
	"sync"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/observation"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// FIXME If you use this package to other project.
	defaultHostname string
	defaultRegion   string
)

func DefaultHostname() string {
	return defaultHostname
}

func DefaultRegion() string {
	return defaultRegion
}

const (
	// MetricTypeGauge indicates a gauge metric.
	MetricTypeGauge = 0
	// MetricTypeCounter indicates a counter metric.
	MetricTypeCounter = 1
)

var metricDescCache sync.Map

// ErrNoData indicates the collector found no data to collect, but had no other error.
var ErrNoData = errors.New("collector returned no data")

// Data is a structure used to define metric data points.
type Data struct {
	name       string
	valueType  int
	Value      float64
	help       string
	labelKey   []string
	labelValue []string
}

// Name returns the metric name without namespace or collector prefixes.
func (d *Data) Name() string {
	return d.name
}

// Type returns the metric value type.
func (d *Data) Type() int {
	return d.valueType
}

// Help returns the metric help text.
func (d *Data) Help() string {
	return d.help
}

// Labels returns a copy of the metric labels.
func (d *Data) Labels() map[string]string {
	labels := make(map[string]string, len(d.labelKey))
	for i, key := range d.labelKey {
		labels[key] = d.labelValue[i]
	}
	return labels
}

// IsNoDataError is a function that checks whether the passed in error is the specific "NoData" error.
func IsNoDataError(err error) bool {
	return errors.Is(err, ErrNoData)
}

func newData(name string, value float64, typ int, help string, labels map[string]string) *Data {
	metadata := observation.Metadata{
		Region:   defaultRegion,
		Hostname: defaultHostname,
	}
	return newMetadataData(name, value, typ, help, &metadata, observation.ScopeHost, labels)
}

func newMetadataData(name string, value float64, typ int, help string,
	metadata *observation.Metadata, scope observation.Scope, labels map[string]string,
) *Data {
	fieldCount := 2
	if scope == observation.ScopeContainer {
		fieldCount = 8
	}

	data := &Data{
		name:       name,
		valueType:  typ,
		Value:      value,
		help:       help,
		labelKey:   make([]string, 0, fieldCount+len(labels)),
		labelValue: make([]string, 0, fieldCount+len(labels)),
	}
	data.labelKey = append(data.labelKey, observation.FieldRegion, observation.FieldHostname)
	data.labelValue = append(data.labelValue, metadata.Region, metadata.Hostname)
	if scope == observation.ScopeContainer {
		data.labelKey = append(data.labelKey,
			observation.FieldContainerID, observation.FieldContainerName,
			observation.FieldContainerHostname, observation.FieldContainerNamespace,
			observation.FieldContainerType, observation.FieldContainerQoS)
		data.labelValue = append(data.labelValue,
			metadata.ContainerID, metadata.ContainerName, metadata.ContainerHostname,
			metadata.ContainerNamespace, metadata.ContainerType, metadata.ContainerQoS)
	}

	extraKeys := make([]string, 0, len(labels))
	for key := range labels {
		// Public identity comes from the selected target, never from business labels.
		if !observation.IsMetadataField(key) {
			extraKeys = append(extraKeys, key)
		}
	}

	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		data.labelKey = append(data.labelKey, key)
		data.labelValue = append(data.labelValue, labels[key])
	}

	return data
}

// NewGaugeData creates a new instance of Data.
//
// Parameters:
//
//	name string - The name of the metric.
//	value float64 - The value of the metric.
//	help string - The help information for the metric, describing its purpose or meaning.
//	label map[string]string - The labels for the metric, used to add additional dimensions to the metric.
//
// Returns:
//
//	*Data - A pointer to the newly created Data instance.
//
// Public metadata comes from the target and cannot be overridden by labels.
func NewGaugeData(name string, value float64, help string, label map[string]string) *Data {
	return newData(name, value, MetricTypeGauge, help, label)
}

// NewCounterData creates a new instance of Data.
//
// Parameters:
//
//	name string - The name of the metric.
//	value float64 - The value of the metric.
//	help string - The help information for the metric, describing its purpose or meaning.
//	label map[string]string - The labels for the metric, used to add additional dimensions to the metric.
//
// Returns:
//
//	*Data - A pointer to the newly created Data instance.
//
// Public metadata comes from the target and cannot be overridden by labels.
func NewCounterData(name string, value float64, help string, label map[string]string) *Data {
	return newData(name, value, MetricTypeCounter, help, label)
}

func newContainerData(container *pod.Container, name string, value float64, typ int, help string, labels map[string]string) *Data {
	metadata := container.ObservationMetadata()
	metadata.Region = defaultRegion
	metadata.Hostname = defaultHostname
	return newMetadataData("container_"+name, value, typ, help, &metadata, observation.ScopeContainer, labels)
}

// NewContainerGaugeData creates a new instance of container Data.
//
// Public metadata comes from the target and cannot be overridden by labels.
func NewContainerGaugeData(container *pod.Container, name string, value float64, help string, label map[string]string) *Data {
	return newContainerData(container, name, value, MetricTypeGauge, help, label)
}

// NewContainerCounterData creates a new instance of container Data.
//
// Public metadata comes from the target and cannot be overridden by labels.
func NewContainerCounterData(container *pod.Container, name string, value float64, help string, label map[string]string) *Data {
	return newContainerData(container, name, value, MetricTypeCounter, help, label)
}

// convert 'Data' to prometheus Metric
func (d *Data) prometheusMetric(collector string) prometheus.Metric {
	var valueType prometheus.ValueType
	switch d.valueType {
	case MetricTypeGauge:
		valueType = prometheus.GaugeValue
	case MetricTypeCounter:
		valueType = prometheus.CounterValue
	default:
		return nil
	}

	metricName := prometheus.BuildFQName(DefaultNamespace, collector, d.name)
	desc, ok := metricDescCache.Load(metricName)
	if !ok {
		desc = prometheus.NewDesc(metricName, d.help, d.labelKey, nil)
		metricDescCache.Store(metricName, desc)
	}

	return prometheus.MustNewConstMetric(
		desc.(*prometheus.Desc),
		valueType,
		d.Value,
		d.labelValue...,
	)
}
