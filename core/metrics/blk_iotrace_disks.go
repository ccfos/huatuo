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

// Startup probes disk identity under sysfs references; userspace publishes
// the Host rows. Device changes invalidate the entire session, so disk
// identity stays cached until the tracing object is closed.
package collector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"

	"golang.org/x/sys/unix"
)

type blkIOTraceQueueProbe = abi.BlkIotraceQueueProbeResult

type blkIOTraceQueueKey = abi.BlkIotraceQueueProbeKey

func (s *blkIOTraceSession) discoverDisks(sysRoot string) error {
	root := filepath.Join(sysRoot, "block")
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read blk_iotrace disks: %w", err)
	}
	s.devices = make(map[[2]uint32]string)
	var probeErr error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "sd") && !strings.HasPrefix(entry.Name(), "nvme") && !strings.HasPrefix(entry.Name(), "vd") {
			continue
		}
		var name blkIOTraceQueueKey
		if len(entry.Name()) >= len(name.Name) {
			probeErr = errors.Join(probeErr, fmt.Errorf("blk_iotrace disk name %q exceeds the kernel name length", entry.Name()))
			continue
		}
		copy(name.Name[:], entry.Name())
		path := filepath.Join(root, entry.Name(), "queue", "iostats")
		probe, err := s.probeDisk(path, name)
		if err != nil {
			probeErr = errors.Join(probeErr, err)
			continue
		}
		if probe.Result != int64(abi.BlkIotraceProbeSupported) {
			continue
		}
		s.devices[[2]uint32{probe.Major, probe.Minor}] = entry.Name()
	}
	return probeErr
}

func (s *blkIOTraceSession) probeDisk(path string, name blkIOTraceQueueKey) (probe blkIOTraceQueueProbe, retErr error) {
	resultMapID := s.object.MapIDByName(blkIOTraceProbeResultMap)
	if err := s.object.WriteMapItems(resultMapID, []bpf.MapItem{{
		Key:   name.Name[:],
		Value: bytesutil.ToBytes(blkIOTraceQueueProbe{}),
	}}); err != nil {
		return probe, fmt.Errorf("prepare blk_iotrace disk result for %s: %w", path, err)
	}
	defer func() {
		if err := s.object.DeleteMapItems(resultMapID, [][]byte{name.Name[:]}); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clear blk_iotrace disk probe %s: %w", path, err))
		}
	}()
	stats, err := os.ReadFile(path)
	if err != nil {
		return probe, fmt.Errorf("read blk_iotrace iostats %s: %w", path, err)
	}
	data, err := s.object.ReadMap(resultMapID, name.Name[:])
	if err != nil {
		return probe, fmt.Errorf("read blk_iotrace queue probe for %s: %w", path, err)
	}
	if err := decodeBPFMapData(data, &probe); err != nil {
		return probe, fmt.Errorf("decode blk_iotrace queue probe for %s: %w", path, err)
	}
	switch probe.Result {
	case int64(abi.BlkIotraceProbeSkipped), int64(abi.BlkIotraceProbeDisabled):
		return probe, nil
	case int64(abi.BlkIotraceProbeSupported):
		iostats, err := strconv.ParseUint(strings.TrimSpace(string(stats)), 10, 64)
		if err != nil {
			return probe, fmt.Errorf("parse blk_iotrace iostats %s: %w", path, err)
		}
		if iostats == 0 {
			probe.Result = int64(abi.BlkIotraceProbeSkipped)
			return probe, nil
		}
		if probe.Disk == 0 {
			return probe, fmt.Errorf("blk_iotrace queue probe for %s has no disk identity", path)
		}
		// Startup discovery is the only Host row creator; Q/C only update it.
		mapID := s.object.MapIDByName(blkDiskBucketMap)
		key := bytesutil.ToBytes(probe.Disk)
		initial := blkIOTraceDiskCounters{
			Major: probe.Major,
			Minor: probe.Minor,
		}
		// Classify at enrollment; C need not inspect the driver's kernel fields.
		if name.Name[0] == 's' && name.Name[1] == 'd' {
			initial.Scsi = 1
		}
		if err := s.object.WriteMapItems(mapID, []bpf.MapItem{{
			Key:   key,
			Value: bytesutil.ToBytes(initial),
		}}); err != nil {
			return probe, fmt.Errorf("register blk_iotrace Host counters for %s: %w", path, err)
		}
		return probe, nil
	case int64(abi.BlkIotraceProbePending):
		return probe, fmt.Errorf("blk_iotrace queue probe for %s did not execute", path)
	default:
		if probe.Result < 0 {
			return probe, fmt.Errorf("blk_iotrace disk probe for %s: %w", path, unix.Errno(-probe.Result))
		}
		return probe, fmt.Errorf("invalid blk_iotrace queue probe result: %d", probe.Result)
	}
}
