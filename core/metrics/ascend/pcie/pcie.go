// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

// Package pcie parses PCIe link capability and status via lspci.
//
// Verify metrics accuracy:
//
//		lspci -s <BDF> -vvv | grep -E "LnkCap:|LnkSta:"
//	 	Exmaple: lspci -s 0000:9D:00.0 -vvv | grep -E "LnkCap:|LnkSta:"
//
// LnkCap Speed/Width → npu_link_cap_speed / npu_link_cap_width
// LnkSta Speed/Width → npu_link_status_speed / npu_link_status_width
package pcie

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// PCIeLinkInfo holds PCIe link capability and status information.
type PCIeLinkInfo struct {
	CapSpeed    float64 // LnkCap speed in GT/s
	CapWidth    uint32  // LnkCap width in lanes
	StatusSpeed float64 // LnkSta speed in GT/s
	StatusWidth uint32  // LnkSta width in lanes
}

var (
	speedRe = regexp.MustCompile(`Speed\s+(\d+\.?\d*)GT/s`)
	widthRe = regexp.MustCompile(`Width\s+x(\d+)`)
)

// GetPCIeLinkInfo runs lspci and parses LnkCap/LnkSta for the given BDF.
func GetPCIeLinkInfo(bdf string) (*PCIeLinkInfo, error) {
	out, err := exec.Command("lspci", "-s", bdf, "-vvv").Output()
	if err != nil {
		return nil, fmt.Errorf("lspci -s %s -vvv failed: %w", bdf, err)
	}

	info := &PCIeLinkInfo{}
	lines := strings.Split(string(out), "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.Contains(trimmed, "LnkCap:") {
			info.CapSpeed, info.CapWidth = parseSpeedWidth(trimmed)
		}
		if strings.Contains(trimmed, "LnkSta:") {
			info.StatusSpeed, info.StatusWidth = parseSpeedWidth(trimmed)
		}
	}

	if info.CapSpeed == 0 && info.StatusSpeed == 0 {
		return nil, fmt.Errorf("lspci: LnkCap/LnkSta not found for %s", bdf)
	}

	return info, nil
}

func parseSpeedWidth(line string) (float64, uint32) {
	var speed float64
	var width uint32

	m := speedRe.FindStringSubmatch(line)
	if len(m) >= 2 {
		speed, _ = strconv.ParseFloat(m[1], 64)
	}

	m = widthRe.FindStringSubmatch(line)
	if len(m) >= 2 {
		w, _ := strconv.ParseUint(m[1], 10, 32)
		width = uint32(w)
	}

	return speed, width
}
