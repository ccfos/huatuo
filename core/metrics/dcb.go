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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"unsafe"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	DCB_CMD_IEEE_GET = 21

	DCB_ATTR_IFNAME        = 1
	DCB_ATTR_IEEE_PFC      = 2
	DCB_ATTR_IEEE_PEER_PFC = 5
	DCB_ATTR_IEEE          = 13

	/* IEEE 802.1Qaz std supported values */
	IEEE_8021QAZ_MAX_TCS = 8
)

type ieeePfc struct { // struct ieee_pfc
	PFCCap      uint8
	PFCEn       uint8
	MBC         uint8
	Delay       uint16
	Requests    [IEEE_8021QAZ_MAX_TCS]uint64 // count of the sent pfc frames
	Indications [IEEE_8021QAZ_MAX_TCS]uint64 // count of the received pfc frames
}

type dcbMsg struct { // struct dcbmsg
	family uint8 //nolint:unused
	cmd    uint8 //nolint:unused
	_pad   uint16
}

func (m *dcbMsg) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type dcbCollector struct {
	dcbNlConn *netlink.Conn
}

func newDcbCollector() *dcbCollector {
	dcbNlConn, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		log.Errorf("dail dcb: %v", err)
		return nil
	}

	return &dcbCollector{
		dcbNlConn: dcbNlConn,
	}
}

func (c *dcbCollector) Close() error {
	return c.dcbNlConn.Close()
}

func (c *dcbCollector) newGetReq(ifname string) (*netlink.Message, error) {
	dcbmsg := &dcbMsg{
		family: uint8(unix.AF_UNSPEC),
		cmd:    uint8(DCB_CMD_IEEE_GET),
	}
	dcbmsgb, err := dcbmsg.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal dcbmsg: %w", err)
	}

	ae := netlink.NewAttributeEncoder()
	ae.String(DCB_ATTR_IFNAME, ifname)
	attrs, err := ae.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode attrs: %w", err)
	}

	return &netlink.Message{
		Header: netlink.Header{
			Type:     unix.RTM_GETDCB,
			Flags:    netlink.Request | netlink.Acknowledge,
			Sequence: 1, // reuse connection must fixd sequence
		},
		Data: append(dcbmsgb, attrs...),
	}, nil
}

func (*dcbCollector) parseIeeePfc(b []byte) (*ieeePfc, error) {
	if len(b) != int(unsafe.Sizeof(ieeePfc{})) {
		return nil, fmt.Errorf("invalid struct ieee_pfc length %d", int(unsafe.Sizeof(ieeePfc{})))
	}

	var pfc ieeePfc
	if err := binary.Read(bytes.NewReader(b), binary.BigEndian, &pfc); err != nil {
		return nil, err
	}
	return &pfc, nil
}

func (c *dcbCollector) getIeeePfc(ifname string) (*ieeePfc, error) {
	req, err := c.newGetReq(ifname)
	if err != nil {
		return nil, err
	}
	msgs, err := c.dcbNlConn.Execute(*req)
	if err != nil {
		return nil, fmt.Errorf("execute netlink: %w", err)
	}

	var ieeepfc *ieeePfc
	msgSize := int(unsafe.Sizeof(dcbMsg{}))
	for _, m := range msgs {
		if len(m.Data) <= msgSize {
			log.Infof("invalid dcbmsg length: %d", msgSize)
			continue
		}

		ad, err := netlink.NewAttributeDecoder(m.Data[msgSize:])
		if err != nil {
			return nil, fmt.Errorf("new attrs decoder: %w", err)
		}
		for ad.Next() {
			switch ad.Type() {
			case DCB_ATTR_IFNAME:
			case DCB_ATTR_IEEE:
				ad.Nested(func(nad *netlink.AttributeDecoder) error {
					for nad.Next() {
						switch nad.Type() {
						case DCB_ATTR_IEEE_PFC:
							ieeepfc, err = c.parseIeeePfc(nad.Bytes())
							return err
						case DCB_ATTR_IEEE_PEER_PFC:
							// TODO: support peer pfc
						}
					}
					return nil
				})
			}
		}
	}

	if ieeepfc != nil {
		return ieeepfc, nil
	}
	return nil, fmt.Errorf("%v: not found ieee pfc", ifname)
}

func init() {
	tracing.RegisterEventTracing("netdev_dcb", newDcb)
}

func newDcb() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: newDcbCollector(),
		Flag:        tracing.FlagMetric,
	}, nil
}

func (pfcc *dcbCollector) Update() ([]*metric.Data, error) {
	type tmp struct {
		ifname string
		*ieeePfc
	}
	var t []*tmp
	for _, ifname := range conf.Get().Tracing.Netdev.Whitelist {
		pfc, err := pfcc.getIeeePfc(ifname)
		if err != nil {
			var opErr *netlink.OpError
			if errors.As(err, &opErr) {
				if errors.Is(opErr.Err, unix.ENODEV) ||
					// virtual iface, such as bond, lo etc.
					errors.Is(opErr.Err, unix.EOPNOTSUPP) {
					log.Debugf("ifname: %v, get ieee pfc: %v", ifname, opErr.Error())
					continue
				}
			}
			log.Warnf("ifname: %v, get ieee pfc: %v", ifname, err)
		} else {
			t = append(t, &tmp{
				ifname:  ifname,
				ieeePfc: pfc,
			})
			log.Debugf("ifname: %s, ieee pfc: %+v", ifname, pfc)
		}
	}

	pfcMetrics := []*metric.Data{}
	for _, data := range t {
		for i, req := range data.Requests {
			pfcMetrics = append(pfcMetrics,
				metric.NewGaugeData("pfc_requests_total", float64(req),
					"count of the sent pfc frames",
					map[string]string{"device": data.ifname, "prio": strconv.Itoa(i)}))
		}
		for i, ind := range data.Indications {
			pfcMetrics = append(pfcMetrics,
				metric.NewGaugeData("pfc_indications_total", float64(ind),
					"count of the received pfc frames",
					map[string]string{"device": data.ifname, "prio": strconv.Itoa(i)}))
		}
	}

	return pfcMetrics, nil
}
