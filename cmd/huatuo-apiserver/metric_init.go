package main

import (
	"huatuo-bamai/pkg/metric/runtime"

	"github.com/prometheus/client_golang/prometheus"
)

var promNamespace = "huatuo_apiserver"

func InitMetricsCollector() (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()

	runtime.RegisterCollector(reg, promNamespace)
	return reg, nil
}
