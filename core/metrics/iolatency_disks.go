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

type ioLatencyQueueProbe = abi.IolatencyQueueProbeResult

type ioLatencyQueueKey = abi.IolatencyQueueProbeKey

func (s *ioLatencySession) discoverDisks(sysRoot string) error {
	root := filepath.Join(sysRoot, "block")
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read iolatency disks: %w", err)
	}
	s.devices = make(map[[2]uint32]string)
	var probeErr error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "sd") && !strings.HasPrefix(entry.Name(), "nvme") && !strings.HasPrefix(entry.Name(), "vd") {
			continue
		}
		var name ioLatencyQueueKey
		if len(entry.Name()) >= len(name.Name) {
			probeErr = errors.Join(probeErr, fmt.Errorf("iolatency disk name %q exceeds the kernel name length", entry.Name()))
			continue
		}
		copy(name.Name[:], entry.Name())
		path := filepath.Join(root, entry.Name(), "queue", "iostats")
		probe, err := s.probeDisk(path, name)
		if err != nil {
			probeErr = errors.Join(probeErr, err)
			continue
		}
		if probe.Result != int64(abi.IOLatencyProbeSupported) {
			continue
		}
		s.devices[[2]uint32{probe.Major, probe.Minor}] = entry.Name()
	}
	return probeErr
}

func (s *ioLatencySession) probeDisk(path string, name ioLatencyQueueKey) (probe ioLatencyQueueProbe, retErr error) {
	resultMapID := s.object.MapIDByName(ioLatencyProbeResultMap)
	if err := s.object.WriteMapItems(resultMapID, []bpf.MapItem{{
		Key:   name.Name[:],
		Value: bytesutil.ToBytes(ioLatencyQueueProbe{}),
	}}); err != nil {
		return probe, fmt.Errorf("prepare iolatency disk result for %s: %w", path, err)
	}
	defer func() {
		if err := s.object.DeleteMapItems(resultMapID, [][]byte{name.Name[:]}); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clear iolatency disk probe %s: %w", path, err))
		}
	}()
	stats, err := os.ReadFile(path)
	if err != nil {
		return probe, fmt.Errorf("read iolatency iostats %s: %w", path, err)
	}
	data, err := s.object.ReadMap(resultMapID, name.Name[:])
	if err != nil {
		return probe, fmt.Errorf("read iolatency queue probe for %s: %w", path, err)
	}
	if err := decodeBPFMapData(data, &probe); err != nil {
		return probe, fmt.Errorf("decode iolatency queue probe for %s: %w", path, err)
	}
	switch probe.Result {
	case int64(abi.IOLatencyProbeSkipped), int64(abi.IOLatencyProbeDisabled):
		return probe, nil
	case int64(abi.IOLatencyProbeSupported):
		iostats, err := strconv.ParseUint(strings.TrimSpace(string(stats)), 10, 64)
		if err != nil {
			return probe, fmt.Errorf("parse iolatency iostats %s: %w", path, err)
		}
		if iostats == 0 {
			probe.Result = int64(abi.IOLatencyProbeSkipped)
			return probe, nil
		}
		if probe.Disk == 0 {
			return probe, fmt.Errorf("iolatency queue probe for %s has no disk identity", path)
		}
		// Startup discovery is the only Host row creator; Q/C only update it.
		mapID := s.object.MapIDByName(blkDiskBucketMap)
		key := bytesutil.ToBytes(probe.Disk)
		initial := ioLatencyDiskCounters{
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
			return probe, fmt.Errorf("register iolatency Host counters for %s: %w", path, err)
		}
		return probe, nil
	case int64(abi.IOLatencyProbePending):
		return probe, fmt.Errorf("iolatency queue probe for %s did not execute", path)
	default:
		if probe.Result < 0 {
			return probe, fmt.Errorf("iolatency disk probe for %s: %w", path, unix.Errno(-probe.Result))
		}
		return probe, fmt.Errorf("invalid iolatency queue probe result: %d", probe.Result)
	}
}
