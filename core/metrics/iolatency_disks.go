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

// Disk identity is cached with Host counters. Registration runs under a
// sysfs-owned kernel reference; retirement drops one disk's interval and
// revokes its Host entry before reclaiming its remaining statistics.
package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/pkg/types"

	"golang.org/x/sys/unix"
)

type ioLatencyQueueProbe struct {
	Result     int64
	Disk       uint64
	Publish    uint64
	Registered uint64
	Initial    ioLatencyDiskCounters
}

// Notifications are coalesced while a scan is in progress. Lost perf records
// also request a scan; a stopped reader falls back to the change sequence
// already collected with BPF health, without restarting unrelated disks.
func (s *ioLatencySession) readDiskEvents(ctx context.Context, reader bpf.PerfEventReader) {
	var change uint32
	for ctx.Err() == nil {
		if err := reader.ReadInto(&change); err != nil {
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				s.requestDiskScan()
				continue
			}
			if ctx.Err() == nil {
				log.Warnf("iolatency: disk notifications unavailable; changes will be checked at collection: %v", err)
			}
			return
		}
		s.requestDiskScan()
	}
}

func (s *ioLatencySession) requestDiskScan() {
	select {
	case s.diskEvents <- struct{}{}:
	default:
	}
}

func (s *ioLatencySession) disarmDiskProbe() error {
	if s.diskProbeName == [32]byte{} {
		return nil
	}
	if err := s.deleteMapKeys(ioLatencyQueueProbeMap, [][]byte{
		s.diskProbeName[:],
	}); err != nil {
		return fmt.Errorf("disarm iolatency disk probe: %w", err)
	}
	s.diskProbeName = [32]byte{}
	return nil
}

func (s *ioLatencySession) refreshDisks(initial bool) error {
	s.disksNeedScan = true
	status, err := s.readStatus()
	if err != nil {
		return err
	}
	disks, err := s.discoverDisks(procfs.DefaultPathByType("sys"), initial)
	if err != nil {
		return err
	}
	if initial && len(disks) == 0 {
		return fmt.Errorf("%w: iolatency found no supported SCSI/NVMe disks", types.ErrTracingStopped)
	}
	// Acknowledge the scan's starting sequence. Changes during discovery
	// remain visible to the next notification or collection health check.
	s.diskChanges = status[1]
	s.disksNeedScan = false
	return nil
}

func (s *ioLatencySession) discoverDisks(sysRoot string, initial bool) (disks []BlkDiskEntry, retErr error) {
	if err := s.disarmDiskProbe(); err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, s.disarmDiskProbe()) }()
	root := filepath.Join(sysRoot, "block")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read iolatency disks: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "sd") && !strings.HasPrefix(entry.Name(), "nvme") {
			continue
		}
		var name [32]byte
		if len(entry.Name()) >= len(name) {
			retErr = errors.Join(retErr, fmt.Errorf("iolatency disk name %q exceeds the kernel name length", entry.Name()))
			continue
		}
		copy(name[:], entry.Name())
		path := filepath.Join(root, entry.Name(), "queue", "iostats")
		probe, err := s.probeDisk(path, name, &ioLatencyQueueProbe{})
		if err != nil {
			if !initial && errors.Is(err, types.ErrTracingStopped) {
				if probe.Registered != 0 {
					s.rememberRetiredDisk(BlkDiskEntry{
						Disk: probe.Disk, Major: probe.Initial.Major, Minor: probe.Initial.Minor,
					})
				}
				log.Warnf("iolatency: disk paused until its timestamp settings are enabled: %v", err)
				continue
			}
			retErr = errors.Join(retErr, err)
			continue
		}
		if probe.Result == 1 || probe.Result == 4 {
			continue
		}
		disk := BlkDiskEntry{Disk: probe.Disk, Major: probe.Initial.Major, Minor: probe.Initial.Minor}
		// Cleanup and enrollment share the collector lock. A retry blocks
		// only this pointer/device number, including after Host removal.
		pendingCleanup := false
		for _, retired := range s.retiredDisks {
			if disk.Disk == retired.Disk ||
				(disk.Major == retired.Major && disk.Minor == retired.Minor) {
				pendingCleanup = true
				break
			}
		}
		if pendingCleanup {
			continue
		}
		if probe.Result != 5 {
			// Keep the candidate until kernel publication and its sysfs read
			// both succeed. A failed read may still have created the row.
			s.rememberRetiredDisk(disk)
			probe.Result, probe.Publish = 0, 1
			published, err := s.probeDisk(path, name, &probe)
			if err != nil {
				retErr = errors.Join(retErr, err)
				continue
			}
			if published.Result != 2 && published.Result != 5 {
				retErr = errors.Join(retErr, fmt.Errorf("iolatency disk changed while registering %s", entry.Name()))
				continue
			}
			delete(s.retiredDisks, disk.Disk)
		}
		disks = append(disks, disk)
	}
	return disks, retErr
}

func (s *ioLatencySession) probeDisk(path string, name [32]byte, command *ioLatencyQueueProbe) (ioLatencyQueueProbe, error) {
	if s.diskProbeName != name {
		if err := s.disarmDiskProbe(); err != nil {
			return *command, err
		}
	}
	mapID := s.object.MapIDByName(ioLatencyQueueProbeMap)
	s.diskProbeName = name
	if err := s.object.WriteMapItems(mapID, []bpf.MapItem{{Key: name[:], Value: bytesutil.ToBytes(command)}}); err != nil {
		return *command, fmt.Errorf("prepare iolatency queue probe for %s: %w", path, err)
	}
	stats, err := os.ReadFile(path)
	if err != nil {
		return *command, fmt.Errorf("read iolatency iostats %s: %w", path, err)
	}
	data, err := s.object.ReadMap(mapID, name[:])
	if err != nil {
		return *command, fmt.Errorf("read iolatency queue probe for %s: %w", path, err)
	}
	var probe ioLatencyQueueProbe
	if err := decodeBPFMapData(data, &probe); err != nil {
		return probe, fmt.Errorf("decode iolatency queue probe for %s: %w", path, err)
	}
	switch probe.Result {
	case 1, 4: // Not a target or awaiting cleanup.
		return probe, nil
	case 2, 5: // Check iostats for both new and registered disks.
		if strings.TrimSpace(string(stats)) != "1" {
			return probe, fmt.Errorf("%w: iolatency unsupported on %s: iostats must be enabled", types.ErrTracingStopped, path)
		}
		return probe, nil
	case 3:
		return probe, fmt.Errorf("%w: iolatency unsupported on %s: request time statistics disabled", types.ErrTracingStopped, path)
	case 0:
		return probe, fmt.Errorf("iolatency queue probe for %s did not execute", path)
	case 6:
		return probe, fmt.Errorf("iolatency disk changed while registering %s", path)
	default:
		if probe.Result < 0 {
			return probe, fmt.Errorf("iolatency disk probe for %s: %w", path, unix.Errno(-probe.Result))
		}
		return probe, fmt.Errorf("invalid iolatency queue probe result: %d", probe.Result)
	}
}

func (s *ioLatencySession) rememberRetiredDisk(disk BlkDiskEntry) {
	if s.retiredDisks == nil {
		s.retiredDisks = make(map[uint64]BlkDiskEntry)
	}
	s.retiredDisks[disk.Disk] = disk
	s.disksNeedScan = true
}

// Device numbers remain the public identity. Drop every row for an affected
// device, including a replacement already enrolled under another pointer,
// so clearing its baseline cannot replay discarded cumulative data.
func (s *ioLatencySession) retireDiskHosts(disks map[uint64]BlkDiskEntry) (map[[2]uint32]bool, error) {
	affected := make(map[[2]uint32]bool, len(s.retiredDisks))
	for _, disk := range s.retiredDisks {
		affected[[2]uint32{disk.Major, disk.Minor}] = true
	}
	if len(affected) == 0 {
		return affected, nil
	}
	var hostKeys [][]byte
	for _, disk := range disks {
		if !affected[[2]uint32{disk.Major, disk.Minor}] {
			continue
		}
		s.rememberRetiredDisk(disk)
		hostKeys = append(hostKeys, bytesutil.ToBytes(disk.Disk))
	}
	if s.previous != nil {
		filterIOLatencyDisks(s.previous, affected)
	}
	return affected, s.deleteMapKeys(blkDiskBucketMap, hostKeys)
}

func (s *ioLatencySession) deleteMapKeys(name string, keys [][]byte) error {
	if len(keys) == 0 {
		return nil
	}
	mapID := s.object.MapIDByName(name)
	err := s.object.DeleteMapItems(mapID, keys)
	if err == nil {
		return nil
	}
	// A batch may stop at a concurrently removed key. Read the remaining
	// keys once, then retry them individually so missing rows cannot block
	// later deletions.
	items, readErr := s.object.DumpMapByName(name)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	selected := make(map[string]bool, len(keys))
	for _, key := range keys {
		selected[string(key)] = true
	}
	var retryErr error
	for _, item := range items {
		if !selected[string(item.Key)] {
			continue
		}
		if err := s.object.DeleteMapItems(mapID, [][]byte{item.Key}); err != nil {
			retryErr = err
		} else {
			delete(selected, string(item.Key))
		}
	}
	if retryErr == nil {
		return nil
	}
	// Confirm failed retries together; a removed row is already settled,
	// while a remaining row keeps its disk or container pending for retry.
	items, readErr = s.object.DumpMapByName(name)
	if readErr != nil {
		return errors.Join(retryErr, readErr)
	}
	for _, item := range items {
		if selected[string(item.Key)] {
			return fmt.Errorf("clean iolatency %s: %w", name, retryErr)
		}
	}
	return nil
}

func filterIOLatencyDisks(snapshot *ioLatencySnapshot, affected map[[2]uint32]bool) {
	for key := range snapshot.host {
		if affected[[2]uint32{key.Major, key.Minor}] {
			delete(snapshot.host, key)
		}
	}
	for key := range snapshot.containers {
		if affected[[2]uint32{key.Major, key.Minor}] {
			delete(snapshot.containers, key)
		}
	}
}
