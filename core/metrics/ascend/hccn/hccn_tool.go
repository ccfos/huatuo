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

// Package hccn collects NPU network metrics via hccn_tool.
//
// Verify metrics:
//
//	/usr/local/Ascend/driver/tools/hccn_tool -i <phyID> -link -g      → npu_network_port_link_status
//	/usr/local/Ascend/driver/tools/hccn_tool -i <phyID> -bandwidth -g → npu_roce_tx_rate / npu_roce_rx_rate
//	/usr/local/Ascend/driver/tools/hccn_tool -i <phyID> -stat -g      → npu_mac_* / npu_roce_*
//	/usr/local/Ascend/driver/tools/hccn_tool -i <phyID> -optical -g   → npu_opt_*
package hccn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	managedexec "huatuo-bamai/internal/exec"
)

// hccnSemaphore limits total concurrent hccn_tool processes across all devices.
var hccnSemaphore = make(chan struct{}, 32)

const (
	hccnTool           = "/usr/local/Ascend/driver/tools/hccn_tool"
	maxHCCNOutputBytes = 1024 * 1024
	maxHCCNErrorBytes  = 4096
)

func getInfoFromHccnTool(args ...string) (string, error) {
	hccnSemaphore <- struct{}{}
	defer func() { <-hccnSemaphore }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return runHCCNCommand(ctx, hccnTool, args...)
}

func runHCCNCommand(ctx context.Context, tool string, args ...string) (string, error) {
	process, err := managedexec.New(managedexec.Spec{
		Path:            tool,
		Args:            args,
		StopGracePeriod: time.Second,
		MaxOutputBytes:  maxHCCNOutputBytes,
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"LD_LIBRARY_PATH=" + os.Getenv("LD_LIBRARY_PATH"),
		},
	})
	if err != nil {
		return "", fmt.Errorf("build hccn_tool command: %w", err)
	}
	if err := process.Run(ctx); err != nil {
		return "", hccnCommandError(
			args,
			process.Stdout(),
			process.Stderr(),
			err,
		)
	}

	return string(process.Stdout()), nil
}

func hccnCommandError(args []string, stdout, stderr []byte, err error) error {
	stdout = bytes.TrimSpace(stdout)
	stderr = bytes.TrimSpace(stderr)
	isTruncated := len(stdout)+len(stderr) > maxHCCNErrorBytes
	if len(stderr) >= maxHCCNErrorBytes {
		stderr = stderr[len(stderr)-maxHCCNErrorBytes:]
		stdout = nil
	} else if isTruncated {
		stdout = stdout[:maxHCCNErrorBytes-len(stderr)]
	}

	errorOutput := ""
	if len(stdout) > 0 {
		errorOutput = "stdout=" + strconv.Quote(string(stdout))
	}
	if len(stderr) > 0 {
		if errorOutput != "" {
			errorOutput += "; "
		}
		errorOutput += "stderr=" + strconv.Quote(string(stderr))
	}
	if errorOutput == "" {
		return fmt.Errorf("hccn_tool %v failed: %w", args, err)
	}
	if isTruncated {
		errorOutput += " (truncated)"
	}
	return fmt.Errorf("hccn_tool %v failed: %w; %s", args, err, errorOutput)
}

// GetLinkStatus returns the link status ("UP" or "DOWN") for the given phyID.
// hccn_tool -i <phyID> -link -g
func GetLinkStatus(phyID int32) (string, error) {
	outStr, err := getInfoFromHccnTool("-i", strconv.Itoa(int(phyID)), "-link", "-g")
	if err != nil {
		return "", err
	}
	replacedStr := strings.ReplaceAll(outStr, "\n", "")
	// Handle boundary values: "Unknown!", "NA", etc.
	if strings.Contains(replacedStr, "Unknown") || strings.Contains(replacedStr, "NA") {
		return "", fmt.Errorf("link status unavailable for phy %d: %s", phyID, replacedStr)
	}
	outArr := strings.Split(replacedStr, " ")
	if len(outArr) != 3 {
		return "", fmt.Errorf("unexpected link status output: %s", outStr)
	}
	return outArr[2], nil
}

// GetInterfaceTraffic returns TX and RX bandwidth in MB/sec.
// hccn_tool -i <phyID> -bandwidth -g
func GetInterfaceTraffic(phyID int32) (tx, rx float64, err error) {
	outStr, err := getInfoFromHccnTool("-i", strconv.Itoa(int(phyID)), "-bandwidth", "-g")
	if err != nil {
		return -1, -1, err
	}
	lines := strings.Split(outStr, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		if strings.Contains(line, "TX:") {
			tx = val
		} else if strings.Contains(line, "RX:") {
			rx = val
		}
	}
	return tx, rx, nil
}

// GetStatInfo returns stat counters for the given phyID.
// hccn_tool -i <phyID> -stat -g
func GetStatInfo(phyID int32) (map[string]int, error) {
	outStr, err := getInfoFromHccnTool("-i", strconv.Itoa(int(phyID)), "-stat", "-g")
	if err != nil {
		return nil, err
	}
	statInfoMap := make(map[string]int)
	lines := strings.Split(outStr, "\n")
	for _, line := range lines {
		parts := strings.Split(line, ":")
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		num, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		statInfoMap[parts[0]] = num
	}
	return statInfoMap, nil
}

// GetOpticalInfo returns optical module info for the given phyID.
// hccn_tool -i <phyID> -optical -g
func GetOpticalInfo(phyID int32) (map[string]float64, error) {
	outStr, err := getInfoFromHccnTool("-i", strconv.Itoa(int(phyID)), "-optical", "-g")
	if err != nil {
		return nil, err
	}
	opticalInfoMap := make(map[string]float64)
	lines := strings.Split(outStr, "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ReplaceAll(strings.TrimSpace(parts[0]), " ", "_")
		// Extract numeric prefix from value, e.g. "44 C" → 44, "3271.40 mV" → 3271.40
		// Non-numeric values like "present", "NA" are skipped.
		valStr := strings.Fields(strings.TrimSpace(parts[1]))
		if len(valStr) == 0 {
			continue
		}
		fval, err := strconv.ParseFloat(valStr[0], 64)
		if err != nil {
			continue
		}
		opticalInfoMap[key] = fval
	}
	return opticalInfoMap, nil
}
