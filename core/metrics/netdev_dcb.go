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

import (
	"errors"
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

type dcbCollector struct{}

func init() {
	tracing.RegisterEventTracing("netdev_dcb", newDcb)
}

func newDcb() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &dcbCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

const (
	DCB_CMD_IEEE_GET       = 21
	DCB_ATTR_IFNAME        = 1
	DCB_ATTR_IEEE_PFC      = 2
	DCB_ATTR_IEEE_PEER_PFC = 5
	DCB_ATTR_IEEE          = 13

	/* IEEE 802.1Qaz std supported values */
	IEEE_8021QAZ_MAX_TCS = 8
)

const (
	sizeofDcbmsg  = 4
	sizeofIEEEPfc = 133
)

type dcbMsg struct {
	family  uint8
	cmd     uint8
	dcb_pad uint16
}

func (msg *dcbMsg) Len() int {
	return sizeofDcbmsg
}

func (msg *dcbMsg) Serialize() []byte {
	return (*(*[sizeofDcbmsg]byte)(unsafe.Pointer(msg)))[:]
}

type ieeePfc struct {
	PFCCap      uint8
	PFCEn       uint8
	MBC         uint8
	Delay       uint16
	Requests    [IEEE_8021QAZ_MAX_TCS]uint64 // count of the sent pfc frames
	Indications [IEEE_8021QAZ_MAX_TCS]uint64 // count of the received pfc frames
}

func deserializeIEEEPfc(b []byte) *ieeePfc {
	return (*ieeePfc)(unsafe.Pointer(&b[0:sizeofIEEEPfc][0]))
}

func doDcbRequest(ifname string) ([][]byte, error) {
	req := nl.NewNetlinkRequest(unix.RTM_GETDCB, 0)
	req.AddData(&dcbMsg{
		family: uint8(unix.AF_UNSPEC),
		cmd:    uint8(DCB_CMD_IEEE_GET),
	})
	req.AddData(nl.NewRtAttr(DCB_ATTR_IFNAME, nl.ZeroTerminated(ifname)))

	return req.Execute(unix.NETLINK_ROUTE, 0)
}

func parseAttributes(attrs []syscall.NetlinkRouteAttr) (*ieeePfc, error) {
	for _, a := range attrs {
		switch a.Attr.Type {
		case DCB_ATTR_IFNAME:
		case DCB_ATTR_IEEE:
			subattrs, err := nl.ParseRouteAttr(a.Value)
			if err != nil {
				return nil, fmt.Errorf("parse attr: %w", err)
			}
			for _, s := range subattrs {
				switch s.Attr.Type {
				case DCB_ATTR_IEEE_PFC:
					return deserializeIEEEPfc(s.Value), nil
				case DCB_ATTR_IEEE_PEER_PFC:
				}
			}
		}
	}

	return nil, fmt.Errorf("no attr")
}

func (dcb *dcbCollector) Update() ([]*metric.Data, error) {
	data := []*metric.Data{}

	for _, ifname := range conf.Get().MetricCollector.NetdevDCB.DeviceList {
		msgs, err := doDcbRequest(ifname)
		if err != nil {
			if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENODEV) {
				continue
			}

			return nil, err
		}

		for _, m := range msgs {
			attrs, err := nl.ParseRouteAttr(m[sizeofDcbmsg:])
			if err != nil {
				return nil, err
			}

			pfc, err := parseAttributes(attrs)
			if err != nil {
				return nil, err
			}

			for i, cnt := range pfc.Requests {
				data = append(data, metric.NewCounterData("pfc_send_total", float64(cnt),
					"count of the sent pfc frames",
					map[string]string{"device": ifname, "prio": strconv.Itoa(i)}))
			}

			for i, cnt := range pfc.Indications {
				data = append(data, metric.NewCounterData("pfc_received_total", float64(cnt),
					"count of the received pfc frames",
					map[string]string{"device": ifname, "prio": strconv.Itoa(i)}))
			}
		}
	}

	return data, nil
}
