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

package metric

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"huatuo-bamai/internal/pod"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// FIXME If you use this package to other project.
	defaultHostname string
	defaultRegion   string
)

const (
	// MetricTypeGauge indicates a gauge metric.
	MetricTypeGauge = 0
	// MetricTypeCounter indicates a counter metric.
	MetricTypeCounter = 1

	// LabelHost indicates the host.
	LabelHost = "host"
	// LabelRegion indicates the data collected from.
	LabelRegion = "region"
	// LabelContainerName indicates the container name.
	LabelContainerName = "container_name"
	// LabelContainerHost indicates the container host.
	LabelContainerHost = "container_host"
	// LabelContainerType indicates the container type.
	LabelContainerType = "container_type"
	// LabelContainerLevel indicates the container level.
	LabelContainerLevel = "container_level"
	// LabelContainerHostNamespace indicates the container host namespace.
	LabelContainerHostNamespace = "container_hostnamespace"
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

// IsNoDataError is a function that checks whether the passed in error is the specific "NoData" error.
func IsNoDataError(err error) bool {
	return errors.Is(err, ErrNoData)
}

func newData(name string, value float64, typ int, help string, label map[string]string) *Data {
	data := &Data{
		name:      name,
		valueType: typ,
		Value:     value,
		help:      help,
	}

	data.labelKey = append(data.labelKey, LabelRegion, LabelHost)
	data.labelValue = append(data.labelValue, defaultRegion, defaultHostname)

	// sort the labelKey
	selfLabelKeys := make([]string, 0, len(label))
	for k := range label {
		selfLabelKeys = append(selfLabelKeys, k)
	}
	sort.Strings(selfLabelKeys)

	// add self label
	for _, k := range selfLabelKeys {
		data.labelKey = append(data.labelKey, k)
		data.labelValue = append(data.labelValue, label[k])
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
// NOTE: the default label `Host` will be added if it is not present in the label map.
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
// NOTE: the default label `Host` will be added if it is not present in the label map.
func NewCounterData(name string, value float64, help string, label map[string]string) *Data {
	return newData(name, value, MetricTypeCounter, help, label)
}

func newContainerData(container *pod.Container, name string, value float64, typ int, help string, label map[string]string) *Data {
	data := &Data{
		name:      fmt.Sprintf("container_%s", name),
		valueType: typ,
		Value:     value,
		help:      help,
	}

	// default label
	data.labelKey = append(data.labelKey,
		LabelRegion,
		LabelContainerHost,
		LabelContainerName,
		LabelContainerType,
		LabelContainerLevel,
		LabelContainerHostNamespace,
		LabelHost)
	data.labelValue = append(data.labelValue,
		defaultRegion,
		container.Hostname,
		container.Name,
		container.Type.String(),
		container.Qos.String(),
		container.LabelHostNamespace(),
		defaultHostname)

	// sort the labelKey
	selfLabelKeys := make([]string, 0, len(label))
	for k := range label {
		selfLabelKeys = append(selfLabelKeys, k)
	}
	sort.Strings(selfLabelKeys)

	// add self label
	for _, k := range selfLabelKeys {
		data.labelKey = append(data.labelKey, k)
		data.labelValue = append(data.labelValue, label[k])
	}

	return data
}

// NewContainerGaugeData creates a new instance of container Data.
//
// NOTE: the default labels 'LabelContainerHost...' will be added if it is not present.
// in the label map.
func NewContainerGaugeData(container *pod.Container, name string, value float64, help string, label map[string]string) *Data {
	return newContainerData(container, name, value, MetricTypeGauge, help, label)
}

// NewContainerCounterData creates a new instance of container Data.
//
// NOTE: the default labels 'LabelContainerHost...' will be added if it is not present.
// in the label map.
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

	metricName := prometheus.BuildFQName(promNamespace, collector, d.name)
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
