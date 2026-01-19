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

package collector

// ref: https://github.com/prometheus/node_exporter/tree/master/collector
//	- sockstat_linux.go

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type sockstatCollector struct{}

func init() {
	tracing.RegisterEventTracing("sockstat", newSockstatCollector)
}

func newSockstatCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &sockstatCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *sockstatCollector) Update() ([]*metric.Data, error) {
	log.Debugf("Updating sockstat metrics")

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	// support the empty container
	if containers == nil {
		containers = make(map[string]*pod.Container)
	}
	// append host into containers
	containers[""] = nil

	var metrics []*metric.Data
	for _, container := range containers {
		m, err := c.procStatMetrics(container)
		if err != nil {
			return nil, fmt.Errorf("couldn't get sockstat metrics for container %v: %w", container, err)
		}
		metrics = append(metrics, m...)
	}

	log.Debugf("Updated sockstat metrics: %v", metrics)
	return metrics, nil
}

func (c *sockstatCollector) procStatMetrics(container *pod.Container) ([]*metric.Data, error) {
	pid := 1 // host
	if container != nil {
		pid = container.InitPid
	}

	// NOTE: non-standard using procfs.NewFS.
	fs, err := procfs.NewFS(filepath.Join(procfs.DefaultProcMountPoint, strconv.Itoa(pid)))
	if err != nil {
		return nil, fmt.Errorf("failed to open procfs: %w", err)
	}

	// If IPv4 and/or IPv6 are disabled on this kernel, handle it gracefully.
	stat, err := fs.NetSockstat()
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		log.Debug("IPv4 sockstat statistics not found, skipping")
	default:
		return nil, fmt.Errorf("failed to get IPv4 sockstat data: %w", err)
	}

	if stat == nil { // nothing to do.
		return nil, nil
	}

	var metrics []*metric.Data

	// If sockstat contains the number of used sockets, export it.
	if stat.Used != nil {
		if container != nil {
			metrics = append(metrics,
				metric.NewContainerGaugeData(container, "sockets_used", float64(*stat.Used), "Number of IPv4 sockets in use.", nil))
		} else {
			metrics = append(metrics,
				metric.NewGaugeData("sockets_used", float64(*stat.Used), "Number of IPv4 sockets in use.", nil))
		}
	}

	// A name and optional value for a sockstat metric.
	type ssPair struct {
		name string
		v    *int
	}

	// Previously these metric names were generated directly from the file output.
	// In order to keep the same level of compatibility, we must map the fields
	// to their correct names.
	for i := range stat.Protocols {
		p := stat.Protocols[i]
		pairs := []ssPair{
			{
				name: "inuse",
				v:    &p.InUse,
			},
			{
				name: "orphan",
				v:    p.Orphan,
			},
			{
				name: "tw",
				v:    p.TW,
			},
			{
				name: "alloc",
				v:    p.Alloc,
			},
			{
				name: "mem",
				v:    p.Mem,
			},
			{
				name: "memory",
				v:    p.Memory,
			},
		}

		// Also export mem_bytes values for sockets which have a mem value
		// stored in pages.
		if p.Mem != nil {
			v := *p.Mem * skMemQuantum
			pairs = append(pairs, ssPair{
				name: "mem_bytes",
				v:    &v,
			})
		}

		for _, pair := range pairs {
			if pair.v == nil {
				// This value is not set for this protocol; nothing to do.
				continue
			}

			// mem, mem_bytes are only for `Host` environment.
			if container != nil && (pair.name == "mem" || pair.name == "mem_bytes") {
				continue
			}

			if container != nil {
				metrics = append(metrics,
					metric.NewContainerGaugeData(container, fmt.Sprintf("%s_%s", p.Protocol, pair.name), float64(*pair.v),
						fmt.Sprintf("Number of %s sockets in state %s.", p.Protocol, pair.name), nil))
			} else {
				metrics = append(metrics,
					metric.NewGaugeData(fmt.Sprintf("%s_%s", p.Protocol, pair.name), float64(*pair.v),
						fmt.Sprintf("Number of %s sockets in state %s.", p.Protocol, pair.name), nil))
			}
		}
	}

	return metrics, nil
}
