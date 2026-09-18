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
	"strings"
	"testing"

	"huatuo-bamai/internal/procfs"
	metricpkg "huatuo-bamai/pkg/metric"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testUdpSnmp = `Ip: Forwarding DefaultTTL InReceives InHdrErrors InAddrErrors ForwDatagrams InUnknownProtos InDiscards InDelivers OutRequests OutDiscards OutNoRoutes ReasmTimeout ReasmReqds ReasmOKs ReasmFails FragOKs FragFails FragCreates
Ip: 1 64 100 0 0 0 0 0 100 200 0 0 0 0 0 0 0 0 0
Icmp: InMsgs InErrors InCsumErrors InDestUnreachs InTimeExcds InParmProbs InSrcQuenchs InRedirects InEchors InEchoReps InTimestamps InTimestampReps InAddrMasks InAddrMaskReps OutMsgs OutErrors OutDestUnreachs OutTimeExcds OutParmProbs OutSrcQuenchs OutRedirects OutEchors OutEchoReps OutTimestamps OutTimestampReps OutAddrMasks OutAddrMaskReps
Icmp: 5 0 0 5 0 0 0 0 0 0 0 0 0 0 5 0 5 0 0 0 0 0 0 0 0 0 0
IcmpMsg: InType3 OutType3
IcmpMsg: 5 5
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 10 5 1 2 3 1000 2000 4 0 5 0
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti
Udp: 1000 2 3 1500 4 5 6 7
UdpLite: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors
UdpLite: 0 0 0 0 0 0 0
`

func newUdpTestCollector(t testing.TB, snmp string) *netUdp {
	t.Helper()

	tmpRoot := t.TempDir()
	netDir := filepath.Join(tmpRoot, "proc", "net")
	require.NoError(t, os.MkdirAll(netDir, 0o755))
	if snmp != "" {
		require.NoError(t, os.WriteFile(
			filepath.Join(netDir, "snmp"), []byte(snmp), 0o600))
	}

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	return &netUdp{}
}

func TestNetUdpUpdate(t *testing.T) {
	collector := newUdpTestCollector(t, testUdpSnmp)

	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 7)

	expected := []struct {
		name       string
		value      float64
		metricType int
	}{
		{name: "datagrams_received_total", value: 1000, metricType: metricpkg.MetricTypeCounter},
		{name: "datagrams_sent_total", value: 1500, metricType: metricpkg.MetricTypeCounter},
		{name: "in_errors_total", value: 3, metricType: metricpkg.MetricTypeCounter},
		{name: "no_ports_total", value: 2, metricType: metricpkg.MetricTypeCounter},
		{name: "rcvbuf_errors_total", value: 4, metricType: metricpkg.MetricTypeCounter},
		{name: "sndbuf_errors_total", value: 5, metricType: metricpkg.MetricTypeCounter},
		{name: "in_csum_errors_total", value: 6, metricType: metricpkg.MetricTypeCounter},
	}
	for i, exp := range expected {
		assert.Equal(t, exp.name, metrics[i].Name())
		assert.Equal(t, exp.value, metrics[i].Value)
		assert.Equal(t, exp.metricType, metrics[i].Type())
	}
}

func TestNetUdpNoDataWithoutUdpSection(t *testing.T) {
	// Drop the Udp section (and everything after it) from the fixture,
	// keeping only the Ip/Icmp/IcmpMsg/Tcp sections.
	noUdp := strings.SplitN(testUdpSnmp, "\nUdp:", 2)[0]
	require.NotContains(t, noUdp, "Udp:")

	collector := newUdpTestCollector(t, noUdp)

	_, err := collector.Update()
	require.ErrorIs(t, err, metricpkg.ErrNoData)
}

func TestNetUdpRejectsMalformedValueLine(t *testing.T) {
	collector := newUdpTestCollector(t, `Udp: InDatagrams NoPorts
Udp:
`)

	_, err := collector.Update()
	require.ErrorContains(t, err, "malformed value line")
}

func TestNetUdpRejectsFieldMismatch(t *testing.T) {
	collector := newUdpTestCollector(t, `Udp: InDatagrams NoPorts InErrors
Udp: 1 2
`)

	_, err := collector.Update()
	require.ErrorContains(t, err, "field mismatch")
}

func TestNetUdpErrorWithoutSnmpFile(t *testing.T) {
	collector := newUdpTestCollector(t, "")

	_, err := collector.Update()
	require.Error(t, err)
}
