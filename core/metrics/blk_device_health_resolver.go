// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Health evidence resolves to one device generation so stacked
// devices cannot cause speculative command fan-out.
package collector

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/blkdevicehealth"
)

var (
	blkDeviceHealthNVMeNamespacePattern  = regexp.MustCompile(`^nvme\d+(?:c(\d+))?n\d+$`)
	blkDeviceHealthNVMeControllerPattern = regexp.MustCompile(`^nvme\d+$`)
)

type blkDeviceHealthResolvedTarget struct {
	eventDevice string
	target      string
	identity    string
	protocol    blkdevicehealth.EvidenceProtocol
	reason      string
}

type blkDeviceHealthResolver struct {
	sysDevBlockPath   string
	sysClassBlockPath string
	sysClassSCSIPath  string
	sysClassNVMePath  string
}

func newBlkDeviceHealthResolver(sysRoot string) blkDeviceHealthResolver {
	return blkDeviceHealthResolver{
		sysDevBlockPath:   filepath.Join(sysRoot, "dev", "block"),
		sysClassBlockPath: filepath.Join(sysRoot, "class", "block"),
		sysClassSCSIPath:  filepath.Join(sysRoot, "class", "scsi_device"),
		sysClassNVMePath:  filepath.Join(sysRoot, "class", "nvme"),
	}
}

func (r blkDeviceHealthResolver) primeNVMeControllerNames() error {
	entries, err := os.ReadDir(r.sysClassNVMePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var firstErr error
	for _, entry := range entries {
		controller := entry.Name()
		if !blkDeviceHealthNVMeControllerPattern.MatchString(controller) {
			continue
		}
		if _, err := os.ReadFile(filepath.Join(
			r.sysClassNVMePath,
			controller,
			"state",
		)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("read %s state: %w", controller, err)
		}
	}
	return firstErr
}

func (r blkDeviceHealthResolver) resolveBlockDevice(dev uint32) blkDeviceHealthResolvedTarget {
	major := dev >> 20
	minor := dev & ((1 << 20) - 1)
	resolved, err := filepath.EvalSymlinks(filepath.Join(
		r.sysDevBlockPath,
		fmt.Sprintf("%d:%d", major, minor),
	))
	if err != nil {
		return blkDeviceHealthResolvedTarget{
			eventDevice: "unknown",
			reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
		}
	}

	device := filepath.Base(resolved)
	if _, err := os.Stat(filepath.Join(resolved, "partition")); err == nil {
		device = filepath.Base(filepath.Dir(resolved))
	}
	return r.resolveBlockName(device)
}

func (r blkDeviceHealthResolver) resolveBlockName(device string) blkDeviceHealthResolvedTarget {
	if device == "" {
		device = "unknown"
	}
	leaves, err := r.blockLeaves(device, make(map[string]bool))
	if err != nil {
		return blkDeviceHealthResolvedTarget{
			eventDevice: device,
			reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
		}
	}
	if len(leaves) != 1 {
		return blkDeviceHealthResolvedTarget{
			eventDevice: device,
			reason:      blkdevicehealth.CollectionReasonTargetUnsupported,
		}
	}

	target := leaves[0]
	if match := blkDeviceHealthNVMeNamespacePattern.FindStringSubmatch(target); match != nil {
		controller, ok := r.nvmeController(target, match[1])
		if !ok {
			return blkDeviceHealthResolvedTarget{
				eventDevice: device,
				reason:      blkdevicehealth.CollectionReasonTargetUnsupported,
			}
		}
		identity := blkdevicehealth.DevicePathIdentity(filepath.Join(
			r.sysClassNVMePath,
			controller,
		))
		if identity == "" {
			return blkDeviceHealthResolvedTarget{
				eventDevice: device,
				reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
			}
		}
		return blkDeviceHealthResolvedTarget{
			eventDevice: device,
			target:      controller,
			identity:    identity,
			protocol:    blkdevicehealth.EvidenceProtocolNVMe,
		}
	}
	if strings.HasPrefix(target, "sd") {
		identity := r.blockIdentity(target)
		if identity == "" {
			return blkDeviceHealthResolvedTarget{
				eventDevice: device,
				reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
			}
		}
		return blkDeviceHealthResolvedTarget{
			eventDevice: device,
			target:      target,
			identity:    identity,
			protocol:    blkdevicehealth.EvidenceProtocolSCSI,
		}
	}
	return blkDeviceHealthResolvedTarget{
		eventDevice: device,
		reason:      blkdevicehealth.CollectionReasonTargetUnsupported,
	}
}

func (r blkDeviceHealthResolver) blockIdentity(device string) string {
	return blkdevicehealth.BlockDeviceIdentity(filepath.Join(r.sysClassBlockPath, device))
}

//nolint:gocritic // The callback contract uses an immutable request snapshot.
func (r blkDeviceHealthResolver) evidenceTargetCurrent(request blkdevicehealth.EvidenceRequest) bool {
	if request.Identity == "" {
		return true
	}
	target := r.resolveBlockName(request.Trigger.Device)
	return target.reason == "" &&
		target.target == request.Target &&
		target.identity == request.Identity
}

func (r blkDeviceHealthResolver) blockLeaves(device string, visiting map[string]bool) ([]string, error) {
	var err error
	device, err = r.wholeDisk(device)
	if err != nil {
		return nil, err
	}
	if visiting[device] {
		return nil, fmt.Errorf("block device dependency cycle at %s", device)
	}
	visiting[device] = true
	defer delete(visiting, device)

	entries, err := os.ReadDir(filepath.Join(r.sysClassBlockPath, device, "slaves"))
	if err != nil {
		if os.IsNotExist(err) {
			return []string{device}, nil
		}
		return nil, err
	}
	if len(entries) == 0 {
		return []string{device}, nil
	}

	unique := make(map[string]bool)
	for _, entry := range entries {
		leaves, err := r.blockLeaves(entry.Name(), visiting)
		if err != nil {
			return nil, err
		}
		for _, leaf := range leaves {
			unique[leaf] = true
		}
	}
	result := make([]string, 0, len(unique))
	for leaf := range unique {
		result = append(result, leaf)
	}
	sort.Strings(result)
	return result, nil
}

func (r blkDeviceHealthResolver) wholeDisk(device string) (string, error) {
	path := filepath.Join(r.sysClassBlockPath, device)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(path, "partition")); err != nil {
		if os.IsNotExist(err) {
			return device, nil
		}
		return "", err
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	parent := filepath.Base(filepath.Dir(resolved))
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return "", fmt.Errorf("resolve whole disk for %s", device)
	}
	return parent, nil
}

func (r blkDeviceHealthResolver) nvmeController(namespace, controllerInstance string) (string, bool) {
	if controllerInstance != "" {
		return "nvme" + controllerInstance, true
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(
		r.sysClassBlockPath,
		namespace,
		"device",
	))
	if err != nil {
		return "", false
	}
	for path := resolved; ; path = filepath.Dir(path) {
		name := filepath.Base(path)
		if blkDeviceHealthNVMeControllerPattern.MatchString(name) {
			return name, true
		}
		if strings.HasPrefix(name, "nvme-subsys") {
			// Multipath heads, including NVMe-loop, belong to the subsystem
			// instead of a controller. Its sysfs links identify controllers;
			// a single one is sufficient without choosing among paths.
			entries, err := os.ReadDir(path)
			if err != nil {
				return "", false
			}
			controller := ""
			for _, entry := range entries {
				if !blkDeviceHealthNVMeControllerPattern.MatchString(entry.Name()) {
					continue
				}
				if controller != "" {
					return "", false
				}
				controller = entry.Name()
			}
			return controller, controller != ""
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
	}
}

func (r blkDeviceHealthResolver) resolveSCSI(host, channel, target, lun uint32) blkDeviceHealthResolvedTarget {
	// Trace-event payloads retain only the low 32 LUN bits. A unique live
	// full LUN can identify the target; aliases must not select another disk.
	prefix := fmt.Sprintf("%d:%d:%d:", host, channel, target)
	hctl := ""
	entries, err := os.ReadDir(r.sysClassSCSIPath)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		fullLUN, parseErr := strconv.ParseUint(strings.TrimPrefix(entry.Name(), prefix), 10, 64)
		if parseErr != nil {
			hctl = ""
			break
		}
		if fullLUN&math.MaxUint32 != uint64(lun) {
			continue
		}
		if hctl != "" {
			hctl = ""
			break
		}
		hctl = entry.Name()
	}
	if err != nil || hctl == "" {
		return blkDeviceHealthResolvedTarget{
			eventDevice: "unknown",
			reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
		}
	}
	blockPath := filepath.Join(r.sysClassSCSIPath, hctl, "device", "block")
	entries, err = os.ReadDir(blockPath)
	if err != nil || len(entries) != 1 {
		return blkDeviceHealthResolvedTarget{
			eventDevice: "unknown",
			reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
		}
	}
	device := entries[0].Name()
	identity := r.blockIdentity(device)
	if identity == "" {
		identity = blkdevicehealth.DevicePathIdentity(filepath.Join(blockPath, device))
	}
	if identity == "" {
		return blkDeviceHealthResolvedTarget{
			eventDevice: device,
			reason:      blkdevicehealth.CollectionReasonTargetUnresolved,
		}
	}
	return blkDeviceHealthResolvedTarget{
		eventDevice: device,
		target:      device,
		identity:    identity,
		protocol:    blkdevicehealth.EvidenceProtocolSCSI,
	}
}
