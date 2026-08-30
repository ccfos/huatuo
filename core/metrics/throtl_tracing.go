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

// This file resolves and validates kernel-specific blk-throttle hooks.

package collector

import (
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/throtl_tracing.c -o $BPF_DIR/throtl_tracing.o

const (
	throtlWaitAggregateMap = "throtl_wait_agg_map"
	throtlTDMap            = "throtl_td_map"
	throtlPendingMap       = "throtl_pending_map"
	throtlStatusMap        = "throtl_stat_map"

	throtlTrackedCallerStart  = "throtl_tracked_caller_start"
	throtlTrackedCallerEnd    = "throtl_tracked_caller_end"
	throtlIgnoredCallerStart  = "throtl_ignored_caller_start"
	throtlIgnoredCallerEnd    = "throtl_ignored_caller_end"
	throtlAddBioSymbol        = "throtl_add_bio_tg"
	throtlPopQueuedSymbol     = "throtl_pop_queued"
	throtlPDFreeSymbol        = "throtl_pd_free"
	throtlExitSymbol          = "blk_throtl_exit"
	throtlTrackedCaller       = "__blk_throtl_bio"
	throtlLegacyTrackedCaller = "blk_throtl_bio"
	throtlIgnoredCaller       = "tg_dispatch_one_bio"
	throtlLegacyExitMarker    = "blkcg_exit_queue"
	throtlMainlineExitMarker  = "blkcg_exit_disk"
)

// Keep reason numbers in sync with enum throtl_failure in the BPF object.
const (
	throtlFailurePendingCollision uint32 = iota + 1
	throtlFailurePendingInsert
	throtlFailurePendingDelete
	throtlFailureOwner
	throtlFailureAggregate
	throtlFailureUnknownCaller
	throtlFailureTimeRollback
	throtlFailurePopState
	throtlFailureLifecycle
)

// A nonzero reason stops this BPF object. Reason and errno share one word.
type throtlStatus struct {
	Errno  int32
	Reason uint32
}

type throtlHooks struct {
	diskProfile  bool
	trackedRange symbol.KsymbolRange
	ignoredRange symbol.KsymbolRange
}

type throtlSession struct {
	object bpf.BPF
}

func (s *throtlSession) readStatus() (throtlStatus, error) {
	mapID := s.object.MapIDByName(throtlStatusMap)
	if mapID == 0 {
		return throtlStatus{}, fmt.Errorf("map %s is unavailable",
			throtlStatusMap)
	}
	value, err := s.object.ReadMap(mapID, []byte{0, 0, 0, 0})
	if err != nil {
		return throtlStatus{}, err
	}

	var packed uint64
	if err := decodeBPFMapData(value, &packed); err != nil {
		return throtlStatus{}, fmt.Errorf("decode %s: %w",
			throtlStatusMap, err)
	}
	return throtlStatus{Errno: int32(packed), Reason: uint32(packed >> 32)}, nil
}

func attachThrotlHooks(
	object bpf.BPF,
	hooks *throtlHooks,
) error {
	exitProgram := "kprobe_blk_throtl_exit_legacy"
	if hooks.diskProfile {
		exitProgram = "kprobe_blk_throtl_exit_mainline"
	}
	options := []bpf.AttachOption{
		{
			ProgramName: exitProgram,
			Symbol:      throtlExitSymbol,
		},
		{
			ProgramName: "kprobe_throtl_pd_free",
			Symbol:      throtlPDFreeSymbol,
		},
		{
			ProgramName: "kretprobe_throtl_pop_queued",
			Symbol:      throtlPopQueuedSymbol,
		},
		{
			ProgramName: "kprobe_throtl_pop_queued",
			Symbol:      throtlPopQueuedSymbol,
		},
		{
			// Attach start last so every observed episode has an exit hook.
			ProgramName: "kprobe_throtl_add_bio_tg",
			Symbol:      throtlAddBioSymbol,
		},
	}

	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach blk-throttle hooks: %w", err)
	}
	return nil
}

func (hooks *throtlHooks) constants() map[string]any {
	return map[string]any{
		throtlTrackedCallerStart: hooks.trackedRange.Start,
		throtlTrackedCallerEnd:   hooks.trackedRange.End,
		throtlIgnoredCallerStart: hooks.ignoredRange.Start,
		throtlIgnoredCallerEnd:   hooks.ignoredRange.End,
	}
}

func loadThrotlHooks() (*throtlHooks, error) {
	hooks, err := resolveThrotlHooks(
		symbol.KsymbolSearchProfile,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve blk-throttle hooks: %w", err)
	}
	return hooks, nil
}

func resolveThrotlHooks(
	search func([]string, []string) (symbol.KsymbolProfile, error),
) (*throtlHooks, error) {
	profile, err := search(
		[]string{
			throtlAddBioSymbol,
			throtlPopQueuedSymbol,
			throtlPDFreeSymbol,
			throtlExitSymbol,
			throtlLegacyExitMarker,
			throtlMainlineExitMarker,
		},
		[]string{
			throtlTrackedCaller,
			throtlLegacyTrackedCaller,
			throtlIgnoredCaller,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("read kernel symbol profile: %w", err)
	}

	trackedSymbol := throtlTrackedCaller
	tracked, modernErr := throtlHookRange(profile, trackedSymbol)
	if modernErr != nil {
		trackedSymbol = throtlLegacyTrackedCaller
		legacy, legacyErr := throtlHookRange(profile, trackedSymbol)
		if legacyErr != nil {
			return nil, fmt.Errorf("%w: resolve tracked caller: %w",
				types.ErrNotSupported, errors.Join(modernErr, legacyErr))
		}
		tracked = legacy
	}

	hooks := &throtlHooks{
		trackedRange: tracked,
	}
	_, legacy := profile.Addresses[throtlLegacyExitMarker]
	_, mainline := profile.Addresses[throtlMainlineExitMarker]
	if legacy == mainline {
		return nil, fmt.Errorf(
			"%w: resolve blk_throtl_exit profile: exactly one of %s and %s is required",
			types.ErrNotSupported,
			throtlLegacyExitMarker,
			throtlMainlineExitMarker,
		)
	}
	hooks.diskProfile = mainline
	ignored, err := throtlHookRange(profile, throtlIgnoredCaller)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ignored caller %s: %w",
			types.ErrNotSupported, throtlIgnoredCaller, err)
	}
	if tracked.Start < ignored.End && ignored.Start < tracked.End {
		return nil, fmt.Errorf("%w: caller ranges %s and %s overlap",
			types.ErrNotSupported, trackedSymbol, throtlIgnoredCaller)
	}
	hooks.ignoredRange = ignored

	for _, name := range []string{
		throtlAddBioSymbol,
		throtlPopQueuedSymbol,
		throtlPDFreeSymbol,
		throtlExitSymbol,
	} {
		_, exists := profile.Addresses[name]
		if !exists {
			return nil, fmt.Errorf("%w: resolve %s: kernel text symbol not found",
				types.ErrNotSupported, name)
		}
	}
	return hooks, nil
}

func throtlHookRange(
	profile symbol.KsymbolProfile,
	name string,
) (symbol.KsymbolRange, error) {
	value, exists := profile.Ranges[name]
	if !exists {
		return symbol.KsymbolRange{}, fmt.Errorf(
			"kernel text symbol %q not found", name)
	}
	return value, nil
}
