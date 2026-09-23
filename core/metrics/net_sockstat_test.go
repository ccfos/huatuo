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
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
)

// The same content is served for the host and container views on purpose: any
// container-scoped series for a host-global field would expose the value read
// from /proc/<pid>/net/sockstat as if it described only that container.
const sockstatFixture = `sockets: used 773
TCP: inuse 30 orphan 7 tw 724 alloc 120 mem 10
UDP: inuse 5 mem 6
UDPLITE: inuse 0
RAW: inuse 0
FRAG: inuse 1 memory 2
`

// writeSockstatFixture points the procfs root at a temporary directory and
// creates proc/<pid>/net/sockstat for each pid. It returns the proc root.
func writeSockstatFixture(t *testing.T, pids ...int) string {
	t.Helper()

	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	procRoot := filepath.Join(tmpRoot, "proc")
	for _, pid := range pids {
		netDir := filepath.Join(procRoot, strconv.Itoa(pid), "net")
		if err := os.MkdirAll(netDir, 0o755); err != nil {
			t.Fatalf("create fixture dir for pid %d: %v", pid, err)
		}
		if err := os.WriteFile(filepath.Join(netDir, "sockstat"),
			[]byte(sockstatFixture), 0o600); err != nil {
			t.Fatalf("write sockstat fixture for pid %d: %v", pid, err)
		}
	}

	return procRoot
}

// newSockstatTestContainer builds the minimal container accepted by
// metric.NewContainerGaugeData: LabelHostNamespace asserts its label entry
// without checking presence, so Labels must carry it.
func newSockstatTestContainer(pid int, name string) *pod.Container {
	return &pod.Container{
		InitPid:  pid,
		Name:     name,
		Hostname: name,
		Type:     pod.ContainerTypeNormal,
		Labels: map[string]any{
			"HostNamespace": "sockstat-test-ns",
		},
	}
}

func sockstatValues(t *testing.T, metrics []*metric.Data) map[string]float64 {
	t.Helper()

	values := make(map[string]float64, len(metrics))
	for _, m := range metrics {
		if _, dup := values[m.Name()]; dup {
			t.Fatalf("duplicate metric %s", m.Name())
		}
		values[m.Name()] = m.Value
	}

	return values
}

func TestSockstatProcStatMetrics(t *testing.T) {
	procRoot := writeSockstatFixture(t, 1, 42)
	// pid 43 has a net directory but no sockstat file, covering kernels with
	// IPv4 disabled.
	if err := os.MkdirAll(filepath.Join(procRoot, "43", "net"), 0o755); err != nil {
		t.Fatalf("create fixture dir for pid 43: %v", err)
	}

	c := &sockstatCollector{}

	t.Run("host keeps global counters", func(t *testing.T) {
		metrics, err := c.procStatMetrics(nil)
		if err != nil {
			t.Fatalf("host procStatMetrics: %v", err)
		}

		want := map[string]float64{
			"sockets_used":  773,
			"TCP_inuse":     30,
			"TCP_orphan":    7,
			"TCP_tw":        724,
			"TCP_alloc":     120,
			"TCP_mem":       10,
			"TCP_mem_bytes": float64(10 * defaultHostPageSize),
			"UDP_inuse":     5,
			"UDP_mem":       6,
			"UDP_mem_bytes": float64(6 * defaultHostPageSize),
			"UDPLITE_inuse": 0,
			"RAW_inuse":     0,
			"FRAG_inuse":    1,
			"FRAG_memory":   2,
		}
		if diff := cmp.Diff(want, sockstatValues(t, metrics)); diff != "" {
			t.Errorf("host metrics mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("container omits host-global counters", func(t *testing.T) {
		metrics, err := c.procStatMetrics(newSockstatTestContainer(42, "sockstat-test"))
		if err != nil {
			t.Fatalf("container procStatMetrics: %v", err)
		}

		// TCP alloc/orphan join mem/mem_bytes as kernel-global counters: the
		// values read through the container PID describe the whole host, so
		// they must not be published as container series. The exact-match
		// comparison also fails if any of them reappears. Container metric
		// names carry the container_ prefix added by newContainerData.
		want := map[string]float64{
			"container_sockets_used":  773,
			"container_TCP_inuse":     30,
			"container_TCP_tw":        724,
			"container_UDP_inuse":     5,
			"container_UDPLITE_inuse": 0,
			"container_RAW_inuse":     0,
			"container_FRAG_inuse":    1,
			"container_FRAG_memory":   2,
		}
		if diff := cmp.Diff(want, sockstatValues(t, metrics)); diff != "" {
			t.Errorf("container metrics mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("missing sockstat file skips gracefully", func(t *testing.T) {
		metrics, err := c.procStatMetrics(newSockstatTestContainer(43, "sockstat-missing"))
		if err != nil {
			t.Fatalf("procStatMetrics with missing sockstat: %v", err)
		}
		if len(metrics) != 0 {
			t.Errorf("want no metrics when sockstat is absent, got %d", len(metrics))
		}
	})
}
