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

package collector

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

// The nf_conntrack table is a fixed-size, host-wide resource shared by every
// network namespace. On hosts that act as NAT gateways or load balancers a
// full table silently drops new flows, so the entry count together with the
// table limit is the minimum needed to alarm on saturation. Both numbers come
// from the two sysctl files below; there is no other procfs interface for
// them. The collector ships disabled - it is listed in the default BlackList of
// huatuo-bamai.conf - so a host only pays for it once an operator enables it.
const (
	conntrackCountPath = "sys/net/netfilter/nf_conntrack_count"
	conntrackMaxPath   = "sys/net/netfilter/nf_conntrack_max"
)

type conntrackCollector struct{}

func init() {
	tracing.RegisterEventTracing("net_conntrack", newConntrackCollector)
}

func newConntrackCollector() (*tracing.EventTracingAttr, error) {
	// The count and max files are exported by the same module, so either one
	// missing means nf_conntrack is unavailable. A kernel built or booted
	// without it reports ENOENT, which must disable the collector instead of
	// failing the whole registration.
	for _, path := range []string{conntrackCountPath, conntrackMaxPath} {
		if err := procfs.RequireFile(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, types.ErrNotSupported
			}

			return nil, fmt.Errorf("check conntrack statistics support (%s): %w", path, err)
		}
	}

	return &tracing.EventTracingAttr{
		TracingData: &conntrackCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// Update reports the raw occupancy and capacity of the table; the fill ratio
// is left to PromQL (entries / entries_limit), so both numbers are exported
// unchanged. The kernel appends a newline to each file, which
// parseutil.ReadUint trims.
func (c *conntrackCollector) Update() ([]*metric.Data, error) {
	count, err := parseutil.ReadUint(procfs.Path(conntrackCountPath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", conntrackCountPath, err)
	}

	limit, err := parseutil.ReadUint(procfs.Path(conntrackMaxPath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", conntrackMaxPath, err)
	}

	return []*metric.Data{
		metric.NewGaugeData("entries", float64(count), "number of entries currently tracked in the nf_conntrack table", nil),
		metric.NewGaugeData("entries_limit", float64(limit), "maximum number of entries the nf_conntrack table can hold", nil),
	}, nil
}
