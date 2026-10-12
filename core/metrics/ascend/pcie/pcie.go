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

// Package pcie reads PCIe link capability and status from sysfs.
//
//	/sys/bus/pci/devices/<BDF>/max_link_speed     → npu_link_cap_speed
//	/sys/bus/pci/devices/<BDF>/max_link_width     → npu_link_cap_width
//	/sys/bus/pci/devices/<BDF>/current_link_speed → npu_link_status_speed
//	/sys/bus/pci/devices/<BDF>/current_link_width → npu_link_status_width
package pcie

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const pciDevicesPath = "/sys/bus/pci/devices"

// PCIeLinkInfo holds PCIe link capability and status information.
type PCIeLinkInfo struct {
	CapSpeed    float64 // LnkCap speed in GT/s
	CapWidth    uint32  // LnkCap width in lanes
	StatusSpeed float64 // LnkSta speed in GT/s
	StatusWidth uint32  // LnkSta width in lanes
}

// GetPCIeLinkInfo reads PCIe link info from sysfs for the given BDF.
func GetPCIeLinkInfo(bdf string) (*PCIeLinkInfo, error) {
	return getPCIeLinkInfo(filepath.Join(pciDevicesPath, bdf))
}

func getPCIeLinkInfo(base string) (*PCIeLinkInfo, error) {
	capSpeed, err := parseSpeed(filepath.Join(base, "max_link_speed"))
	if err != nil {
		return nil, fmt.Errorf("read maximum link speed: %w", err)
	}
	capWidth, err := parseWidth(filepath.Join(base, "max_link_width"))
	if err != nil {
		return nil, fmt.Errorf("read maximum link width: %w", err)
	}
	statusSpeed, err := parseSpeed(filepath.Join(base, "current_link_speed"))
	if err != nil {
		return nil, fmt.Errorf("read current link speed: %w", err)
	}
	statusWidth, err := parseWidth(filepath.Join(base, "current_link_width"))
	if err != nil {
		return nil, fmt.Errorf("read current link width: %w", err)
	}

	return &PCIeLinkInfo{
		CapSpeed:    capSpeed,
		CapWidth:    capWidth,
		StatusSpeed: statusSpeed,
		StatusWidth: statusWidth,
	}, nil
}

func parseSpeed(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// Format: "16 GT/s" or "16.0 GT/s" or "2.5 GT/s\n"
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty speed in %s", path)
	}
	speed, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(speed) || math.IsInf(speed, 0) || speed <= 0 {
		return 0, fmt.Errorf("invalid speed in %s: %q", path, fields[0])
	}
	return speed, nil
}

func parseWidth(path string) (uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// Format: "16\n" or "8\n"
	width, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid width in %s: %w", path, err)
	}
	if width == 0 {
		return 0, fmt.Errorf("invalid width in %s: must be greater than zero", path)
	}
	return uint32(width), nil
}
