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

// Reuse completed bio cache entries on the IO path and reclaim old inactive
// entries periodically. Userspace selects candidates; BPF serializes deletion
// against reactivation and rechecks the observed idle timestamp.
package collector

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"

	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
)

const (
	ioLatencyBioStates = 10240
	// Retain inactive entries for one sweep interval to allow address reuse.
	ioLatencyGCInterval = time.Minute
)

type ioLatencyBioState struct {
	QueueNS    uint64
	Blkcg      uint64
	Major      uint32
	Minor      uint32
	InactiveNS uint64
	Guard      uint32
	Deleted    uint32
}

type ioLatencyGCJob struct {
	Bio        uint64
	InactiveNS uint64
	CutoffNS   uint64
}

type ioLatencyGC struct {
	object *ebpf.Collection
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iolatency_gc.c -o $BPF_DIR/iolatency_gc.o

func newIOLatencyGC(object bpf.BPF) (_ *ioLatencyGC, err error) {
	gc := &ioLatencyGC{}
	defer func() {
		if err != nil {
			gc.close()
		}
	}()
	if err := unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}); err != nil {
		return nil, fmt.Errorf("set bio GC memlock limit: %w", err)
	}
	// The maintenance object shares only the tracing object's bio cache.
	cache, err := ebpf.NewMapFromID(ebpf.MapID(object.MapIDByName("bio_latency_map")))
	if err != nil {
		return nil, fmt.Errorf("open bio cache: %w", err)
	}
	defer cache.Close()
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(bpf.DefaultObjDir, "iolatency_gc.o"))
	if err != nil {
		return nil, fmt.Errorf("read bio GC object: %w", err)
	}
	gc.object, err = ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{
		MapReplacements: map[string]*ebpf.Map{
			"bio_latency_map": cache,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("load bio GC object: %w", err)
	}
	// Verify local BPF permissions and test-run support before attaching IO hooks.
	if err := gc.reclaim(ioLatencyGCJob{}); err != nil {
		return nil, fmt.Errorf("probe bio GC: %w", err)
	}
	return gc, nil
}

func (gc *ioLatencyGC) close() {
	if gc.object != nil {
		gc.object.Close()
	}
}

func (gc *ioLatencyGC) sweep(ctx context.Context) error {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		return err
	}
	if now.Nano() < int64(ioLatencyGCInterval) {
		return nil
	}
	cutoff := uint64(now.Nano() - int64(ioLatencyGCInterval))
	return reclaimIOLatencyBios(ctx, gc.object.Maps["bio_latency_map"].Iterate(), cutoff, gc.reclaim)
}

type ioLatencyBioIterator interface {
	Next(key, value any) bool
	Err() error
}

func reclaimIOLatencyBios(
	ctx context.Context,
	iterator ioLatencyBioIterator,
	cutoff uint64,
	reclaim func(ioLatencyGCJob) error,
) error {
	var key uint64
	var state ioLatencyBioState
	var jobs []ioLatencyGCJob
	// This worker is the cache's only deleter. Select before deleting so a
	// removed hash cursor cannot restart traversal; the pass is capacity-bounded.
	for scanned := 0; scanned < ioLatencyBioStates; scanned++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !iterator.Next(&key, &state) {
			break
		}
		if state.QueueNS == 0 && state.InactiveNS != 0 && state.InactiveNS <= cutoff {
			jobs = append(jobs, ioLatencyGCJob{
				Bio: key, InactiveNS: state.InactiveNS, CutoffNS: cutoff,
			})
		}
	}
	if err := iterator.Err(); err != nil {
		return err
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reclaim(job); err != nil {
			return err
		}
	}
	return nil
}

func (gc *ioLatencyGC) reclaim(job ioLatencyGCJob) error {
	// SCHED_CLS test-run carries the job after Ethernet/IPv4 headers.
	const jobOffset = 14 + 20
	packet := [64]byte{12: 0x08, 14: 0x45, 17: 50}
	binary.NativeEndian.PutUint64(packet[jobOffset:], job.Bio)
	binary.NativeEndian.PutUint64(packet[jobOffset+8:], job.InactiveNS)
	binary.NativeEndian.PutUint64(packet[jobOffset+16:], job.CutoffNS)
	result, err := gc.object.Programs["bio_gc_run"].Run(&ebpf.RunOptions{Data: packet[:], Repeat: 1})
	if err != nil {
		return err
	}
	switch int32(result) {
	case 0, 1, -int32(unix.EBUSY):
		// Reclaimed, no longer eligible, or contended: a later pass can retry.
		return nil
	default:
		return fmt.Errorf("reclaim bio %#x: %w", job.Bio, unix.Errno(-int32(result)))
	}
}
