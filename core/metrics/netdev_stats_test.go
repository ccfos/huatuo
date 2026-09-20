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
	"testing"

	"huatuo-bamai/internal/procfs"

	"github.com/jsimonetti/rtnetlink"
)

func TestNetdevBackendsExportSharedCountersUnderIdenticalNames(t *testing.T) {
	linkStats := &rtnetlink.LinkStats64{
		RXPackets:       10,
		TXPackets:       11,
		RXBytes:         12,
		TXBytes:         13,
		RXErrors:        14,
		TXErrors:        15,
		RXDropped:       16,
		TXDropped:       17,
		Multicast:       18,
		Collisions:      19,
		RXFrameErrors:   20,
		RXFIFOErrors:    21,
		TXCarrierErrors: 22,
		TXFIFOErrors:    23,
		RXCompressed:    24,
		TXCompressed:    25,
	}

	procLine := &procfs.NetDevLine{
		RxPackets:    10,
		TxPackets:    11,
		RxBytes:      12,
		TxBytes:      13,
		RxErrors:     14,
		TxErrors:     15,
		RxDropped:    16,
		TxDropped:    17,
		RxMulticast:  18,
		TxCollisions: 19,
		RxFrame:      20,
		RxFIFO:       21,
		TxCarrier:    22,
		TxFIFO:       23,
		RxCompressed: 24,
		TxCompressed: 25,
	}

	netlink := netlinkDeviceStats(linkStats)
	proc := procDeviceStats(procLine)

	// Both backends feed the same emitter, which appends "_total". Every
	// counter exported by the default proc backend must keep its name when
	// netlink is enabled, otherwise toggling EnableNetlink silently renames
	// the series and breaks dashboards and rate() expressions.
	for name, want := range proc {
		got, ok := netlink[name]
		if !ok {
			t.Errorf("counter %q exported by the proc backend is missing from the netlink backend", name)
			continue
		}
		if got != want {
			t.Errorf("counter %q: netlink value = %d, proc value = %d", name, got, want)
		}
	}
}
