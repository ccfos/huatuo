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
	"math"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/qdisc"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
)

func TestQdiscCollectorUpdate(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.Qdisc.DeviceIncluded = `^eth[01]$`
	testConfig.Qdisc.DeviceExcluded = `^eth1$`
	Set(testConfig)

	attr, err := newQdiscCollector()
	if err != nil {
		t.Fatalf("create qdisc collector: %v", err)
	}
	collector, ok := attr.TracingData.(*qdiscCollector)
	if !ok {
		t.Fatalf("tracing data type = %T, want *qdiscCollector", attr.TracingData)
	}
	collector.readStats = func() ([]qdisc.Stats, error) {
		return []qdisc.Stats{
			{
				Netdev:       "eth0",
				Parent:       math.MaxUint32,
				Kind:         "fq_codel",
				Bytes:        1,
				Packets:      2,
				Drops:        3,
				Requeues:     4,
				Overlimits:   5,
				QueueLength:  6,
				BacklogBytes: 7,
			},
			{Netdev: "eth1", Parent: math.MaxUint32, Kind: "fq"},
			{Netdev: "eth0", Parent: 1, Kind: "fq"},
			{Netdev: "eth0", Parent: math.MaxUint32, Kind: "noqueue"},
			{Netdev: "lo", Parent: math.MaxUint32, Kind: "fq"},
		}, nil
	}

	metrics, err := collector.Update()
	if err != nil {
		t.Fatalf("update qdisc metrics: %v", err)
	}
	want := []qdiscMetric{
		{Name: "bytes_total", Value: 1, Device: "eth0", Kind: "fq_codel"},
		{Name: "packets_total", Value: 2, Device: "eth0", Kind: "fq_codel"},
		{Name: "drops_total", Value: 3, Device: "eth0", Kind: "fq_codel"},
		{Name: "requeues_total", Value: 4, Device: "eth0", Kind: "fq_codel"},
		{Name: "overlimits_total", Value: 5, Device: "eth0", Kind: "fq_codel"},
		{Name: "current_queue_length", Value: 6, Device: "eth0", Kind: "fq_codel"},
		{Name: "backlog", Value: 7, Device: "eth0", Kind: "fq_codel"},
	}
	if diff := cmp.Diff(want, summarizeQdiscMetrics(metrics)); diff != "" {
		t.Fatalf("qdisc metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestQdiscCollectorUpdateReadError(t *testing.T) {
	wantErr := errors.New("netlink unavailable")
	collector := &qdiscCollector{
		readStats: func() ([]qdisc.Stats, error) { return nil, wantErr },
	}

	_, err := collector.Update()
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapping %v", err, wantErr)
	}
}

func TestNewQdiscCollectorRejectsInvalidDeviceFilter(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.Qdisc.DeviceIncluded = "["
	Set(testConfig)

	_, err := newQdiscCollector()
	if err == nil || !strings.Contains(err.Error(), "qdisc device filter") {
		t.Fatalf("error = %v, want invalid qdisc device filter", err)
	}
}

func BenchmarkQdiscCollectorUpdate(b *testing.B) {
	stats := make([]qdisc.Stats, 128)
	for i := range stats {
		stats[i] = qdisc.Stats{
			Netdev:       "eth0",
			Parent:       math.MaxUint32,
			Kind:         "fq_codel",
			Bytes:        1_024,
			Packets:      128,
			Drops:        1,
			QueueLength:  2,
			BacklogBytes: 4_096,
		}
	}
	collector := &qdiscCollector{
		readStats: func() ([]qdisc.Stats, error) { return stats, nil },
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := collector.Update(); err != nil {
			b.Fatalf("update qdisc metrics: %v", err)
		}
	}
}

type qdiscMetric struct {
	Name   string
	Value  float64
	Device string
	Kind   string
}

func summarizeQdiscMetrics(metrics []*metric.Data) []qdiscMetric {
	summary := make([]qdiscMetric, 0, len(metrics))
	for _, data := range metrics {
		labels := data.Labels()
		summary = append(summary, qdiscMetric{
			Name:   data.Name(),
			Value:  data.Value,
			Device: labels["device"],
			Kind:   labels["kind"],
		})
	}
	return summary
}

// Keep netdev_dcb cases in this existing file instead of adding one unit test
// file per collector.
func TestDeserializeIEEEPfc(t *testing.T) {
	t.Parallel()

	pfc := testIEEEPfc()
	got, err := deserializeIEEEPfc(append([]byte(nil), ieeePfcBytes(&pfc)...))
	require.NoError(t, err)
	require.Equal(t, pfc, *got)

	_, err = deserializeIEEEPfc(make([]byte, int(unsafe.Sizeof(ieeePfc{}))-1))
	require.ErrorContains(t, err, "ieee pfc attr too short")
}

func TestParseAttributes(t *testing.T) {
	t.Parallel()

	pfc := testIEEEPfc()
	pfcAttr := nl.NewRtAttr(DCB_ATTR_IEEE_PFC, ieeePfcBytes(&pfc)).Serialize()
	peerPfcAttr := nl.NewRtAttr(DCB_ATTR_IEEE_PEER_PFC, ieeePfcBytes(&pfc)).Serialize()

	tests := []struct {
		name    string
		raw     []byte
		want    *ieeePfc
		wantErr string
	}{
		{
			name: "pfc attribute",
			raw: append(
				nl.NewRtAttr(DCB_ATTR_IFNAME, nl.ZeroTerminated("eth0")).Serialize(),
				nl.NewRtAttr(DCB_ATTR_IEEE, pfcAttr).Serialize()...,
			),
			want: &pfc,
		},
		{
			name: "peer pfc is ignored",
			raw: append(
				nl.NewRtAttr(DCB_ATTR_IEEE, peerPfcAttr).Serialize(),
				nl.NewRtAttr(DCB_ATTR_IEEE, pfcAttr).Serialize()...,
			),
			want: &pfc,
		},
		{
			name:    "missing ieee attribute",
			raw:     nl.NewRtAttr(DCB_ATTR_IFNAME, nl.ZeroTerminated("eth0")).Serialize(),
			wantErr: "no attr",
		},
		{
			name:    "no pfc attribute",
			raw:     nl.NewRtAttr(DCB_ATTR_IEEE, peerPfcAttr).Serialize(),
			wantErr: "no attr",
		},
		{
			name: "short pfc attribute",
			raw: nl.NewRtAttr(DCB_ATTR_IEEE,
				nl.NewRtAttr(DCB_ATTR_IEEE_PFC, []byte{0, 0, 0, 0}).Serialize(),
			).Serialize(),
			wantErr: "ieee pfc attr too short",
		},
		{
			name:    "malformed nested attribute",
			raw:     nl.NewRtAttr(DCB_ATTR_IEEE, []byte{0xff, 0xff, 0, 0}).Serialize(),
			wantErr: "parse attr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			attrs, err := nl.ParseRouteAttr(tt.raw)
			require.NoError(t, err)

			got, err := parseAttributes(attrs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, *tt.want, *got)
		})
	}
}

func TestDcbCollectorUpdate(t *testing.T) {
	pfc := testIEEEPfc()
	collector := &dcbCollector{
		deviceMatcher: mustDcbMatcher(t, `^eth[01]$`),
		listDevices: func() ([]string, error) {
			return []string{"lo", "eth1", "eth0"}, nil
		},
		query: func(ifname string) ([][]byte, error) {
			if ifname != "eth0" {
				return nil, unix.EOPNOTSUPP
			}
			msg := make([]byte, sizeofDcbmsg)
			msg = append(msg, dcbRawAttrs(&pfc)...)
			return [][]byte{msg}, nil
		},
	}

	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 2*IEEE_8021QAZ_MAX_TCS)

	samples := make(map[string]dcbSample, len(metrics))
	for _, data := range metrics {
		labels := data.Labels()
		key := data.Name() + "/" + labels["prio"]
		require.NotContains(t, samples, key, "duplicate sample %s", key)
		samples[key] = dcbSample{Device: labels["device"], Value: data.Value}
	}

	for prio := 0; prio < IEEE_8021QAZ_MAX_TCS; prio++ {
		key := "pfc_send_total/" + strconv.Itoa(prio)
		sample, ok := samples[key]
		require.True(t, ok, "missing %s", key)
		assert.Equal(t, "eth0", sample.Device)
		assert.Equal(t, float64(10+prio), sample.Value)

		key = "pfc_received_total/" + strconv.Itoa(prio)
		sample, ok = samples[key]
		require.True(t, ok, "missing %s", key)
		assert.Equal(t, "eth0", sample.Device)
		assert.Equal(t, float64(100+prio), sample.Value)
	}
}

func TestDcbCollectorUpdateDeviceListError(t *testing.T) {
	wantErr := errors.New("sysfs unavailable")
	collector := &dcbCollector{
		deviceMatcher: mustDcbMatcher(t, `.*`),
		listDevices:   func() ([]string, error) { return nil, wantErr },
		query: func(string) ([][]byte, error) {
			t.Fatal("query must not run when device discovery fails")
			return nil, nil
		},
	}

	_, err := collector.Update()
	require.ErrorIs(t, err, wantErr)
}

func TestDcbCollectorUpdateSkipsUnsupportedDevices(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "not supported", err: unix.EOPNOTSUPP},
		{name: "device gone", err: unix.ENODEV},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &dcbCollector{
				deviceMatcher: mustDcbMatcher(t, `^eth0$`),
				listDevices:   func() ([]string, error) { return []string{"eth0"}, nil },
				query:         func(string) ([][]byte, error) { return nil, tt.err },
			}

			metrics, err := collector.Update()
			require.NoError(t, err)
			assert.Empty(t, metrics)
		})
	}
}

func TestDcbCollectorUpdatePropagatesQueryError(t *testing.T) {
	collector := &dcbCollector{
		deviceMatcher: mustDcbMatcher(t, `^eth0$`),
		listDevices:   func() ([]string, error) { return []string{"eth0"}, nil },
		query:         func(string) ([][]byte, error) { return nil, unix.EPERM },
	}

	_, err := collector.Update()
	require.ErrorIs(t, err, unix.EPERM)
}

func TestDcbCollectorUpdatePropagatesParseError(t *testing.T) {
	tests := []struct {
		name    string
		attrs   []byte
		wantErr string
	}{
		{name: "missing pfc", wantErr: "no attr"},
		{
			name:    "malformed ieee",
			attrs:   nl.NewRtAttr(DCB_ATTR_IEEE, []byte{0xff, 0xff, 0, 0}).Serialize(),
			wantErr: "parse attr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := &dcbCollector{
				deviceMatcher: mustDcbMatcher(t, `^eth0$`),
				listDevices:   func() ([]string, error) { return []string{"eth0"}, nil },
				query: func(string) ([][]byte, error) {
					return [][]byte{append(make([]byte, sizeofDcbmsg), tt.attrs...)}, nil
				},
			}

			_, err := collector.Update()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNewDcbRejectsInvalidDeviceFilter(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.NetdevDCB.DeviceList = []string{"["}
	Set(testConfig)

	_, err := newDcb()
	require.ErrorContains(t, err, "netdev dcb device list")
}

func TestNewDcbWiresCollector(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.NetdevDCB.DeviceList = []string{"eth0"}
	Set(testConfig)

	attr, err := newDcb()
	require.NoError(t, err)
	require.NotNil(t, attr)
	assert.Equal(t, tracing.FlagMetric, attr.Flag)

	collector, ok := attr.TracingData.(*dcbCollector)
	require.True(t, ok)
	require.NotNil(t, collector.deviceMatcher)
	require.NotNil(t, collector.listDevices)
	require.NotNil(t, collector.query)
}

type dcbSample struct {
	Device string
	Value  float64
}

func mustDcbMatcher(t *testing.T, pattern string) *matcher.ListMatcher {
	t.Helper()

	m, err := matcher.NewListMatcher([]string{pattern})
	require.NoError(t, err)
	return m
}

func testIEEEPfc() ieeePfc {
	pfc := ieeePfc{PFCCap: 1, PFCEn: 2, MBC: 3, Delay: 4}
	for i := range pfc.Requests {
		pfc.Requests[i] = uint64(10 + i)
		pfc.Indications[i] = uint64(100 + i)
	}
	return pfc
}

func ieeePfcBytes(pfc *ieeePfc) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(pfc)), int(unsafe.Sizeof(*pfc)))
}

func dcbRawAttrs(pfc *ieeePfc) []byte {
	ieee := nl.NewRtAttr(DCB_ATTR_IEEE, nil)
	ieee.AddRtAttr(DCB_ATTR_IEEE_PFC, ieeePfcBytes(pfc))
	return append(
		nl.NewRtAttr(DCB_ATTR_IFNAME, nl.ZeroTerminated("eth0")).Serialize(),
		ieee.Serialize()...,
	)
}
