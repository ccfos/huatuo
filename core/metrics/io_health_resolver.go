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

package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"huatuo-bamai/internal/ioobserve/health"
)

const (
	ioHealthProtocolNVMe = "nvme"
	ioHealthProtocolSCSI = "scsi"
)

var (
	ioHealthNVMeNamespacePattern  = regexp.MustCompile(`^nvme[0-9]+(?:c([0-9]+))?n[0-9]+$`)
	ioHealthNVMeControllerPattern = regexp.MustCompile(`^nvme[0-9]+$`)
)

type ioHealthResolvedTarget struct {
	eventDevice string
	target      string
	protocol    string
	reason      string
}

type ioHealthResolver struct {
	sysDevBlockPath   string
	sysClassBlockPath string
	sysClassSCSIPath  string
}

func newIOHealthResolver(sysRoot string) ioHealthResolver {
	return ioHealthResolver{
		sysDevBlockPath:   filepath.Join(sysRoot, "dev", "block"),
		sysClassBlockPath: filepath.Join(sysRoot, "class", "block"),
		sysClassSCSIPath:  filepath.Join(sysRoot, "class", "scsi_device"),
	}
}

func (r ioHealthResolver) resolveBlockDevice(dev uint32) ioHealthResolvedTarget {
	major := dev >> 20
	minor := dev & ((1 << 20) - 1)
	resolved, err := filepath.EvalSymlinks(filepath.Join(
		r.sysDevBlockPath,
		fmt.Sprintf("%d:%d", major, minor),
	))
	if err != nil {
		return ioHealthResolvedTarget{
			eventDevice: "unknown",
			reason:      health.CollectionReasonTargetUnresolved,
		}
	}

	device := filepath.Base(resolved)
	if _, err := os.Stat(filepath.Join(resolved, "partition")); err == nil {
		device = filepath.Base(filepath.Dir(resolved))
	}
	return r.resolveBlockName(device)
}

func (r ioHealthResolver) resolveBlockName(device string) ioHealthResolvedTarget {
	if device == "" {
		device = "unknown"
	}
	leaves, err := r.blockLeaves(device, make(map[string]bool))
	if err != nil {
		return ioHealthResolvedTarget{
			eventDevice: device,
			reason:      health.CollectionReasonTargetUnresolved,
		}
	}
	if len(leaves) != 1 {
		return ioHealthResolvedTarget{
			eventDevice: device,
			reason:      health.CollectionReasonTargetUnsupported,
		}
	}

	target := leaves[0]
	if match := ioHealthNVMeNamespacePattern.FindStringSubmatch(target); match != nil {
		controller, ok := r.nvmeController(target, match[1])
		if !ok {
			return ioHealthResolvedTarget{
				eventDevice: device,
				reason:      health.CollectionReasonTargetUnsupported,
			}
		}
		return ioHealthResolvedTarget{
			eventDevice: device,
			target:      controller,
			protocol:    ioHealthProtocolNVMe,
		}
	}
	if strings.HasPrefix(target, "sd") {
		return ioHealthResolvedTarget{
			eventDevice: device,
			target:      target,
			protocol:    ioHealthProtocolSCSI,
		}
	}
	return ioHealthResolvedTarget{
		eventDevice: device,
		reason:      health.CollectionReasonTargetUnsupported,
	}
}

func (r ioHealthResolver) blockLeaves(device string, visiting map[string]bool) ([]string, error) {
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

func (r ioHealthResolver) wholeDisk(device string) (string, error) {
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

func (r ioHealthResolver) nvmeController(namespace, controllerInstance string) (string, bool) {
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
		if ioHealthNVMeControllerPattern.MatchString(name) {
			return name, true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
	}
}

func (r ioHealthResolver) resolveSCSI(host, channel, target, lun uint32) ioHealthResolvedTarget {
	hctl := fmt.Sprintf("%d:%d:%d:%d", host, channel, target, lun)
	entries, err := os.ReadDir(filepath.Join(r.sysClassSCSIPath, hctl, "device", "block"))
	if err != nil || len(entries) != 1 {
		return ioHealthResolvedTarget{
			eventDevice: "unknown",
			reason:      health.CollectionReasonTargetUnresolved,
		}
	}
	device := entries[0].Name()
	return ioHealthResolvedTarget{
		eventDevice: device,
		target:      device,
		protocol:    ioHealthProtocolSCSI,
	}
}
