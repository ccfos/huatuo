// Copyright 2026 The HuaTuo Authors
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

//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
)

const (
	eventMap     = "cgroup_perf_events"
	eventBuffer  = 8192
	eventTimeout = 10 * time.Second
	syncObject   = "cgroup_css_sync.o"
	eventsObject = "cgroup_css_events.o"
	syncProgram  = "bpf_cgroup_subsys_state_prog"
)

type config struct {
	bpfDir     string
	syncNotify string
	eventID    string
	syncSymbol string
	readyFile  string
}

type eventRecord struct {
	Source      string `json:"source"`
	Operation   string `json:"operation"`
	KnodeName   string `json:"knode_name"`
	Cgroup      string `json:"cgroup"`
	CgroupRoot  int32  `json:"cgroup_root"`
	CgroupLevel int32  `json:"cgroup_level"`
	CSSCount    int    `json:"css_count"`
}

type eventRequest struct {
	target   string
	source   string
	expected map[abi.CgroupCSSOperation]string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cgroup CSS probe: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 6 {
		return errors.New("usage: cgroup_css_probe BPF_DIR SYNC_NOTIFY EVENT_ID SYNC_SYMBOL READY_FILE")
	}
	cfg := config{
		bpfDir:     os.Args[1],
		syncNotify: os.Args[2],
		eventID:    os.Args[3],
		syncSymbol: os.Args[4],
		readyFile:  os.Args[5],
	}

	if err := bpf.Init(nil); err != nil {
		return fmt.Errorf("initialize BPF: %w", err)
	}
	defer bpf.Shutdown()

	bpf.DefaultObjDir = cfg.bpfDir
	encoder := json.NewEncoder(os.Stdout)
	if err := collectSyncEvent(cfg, encoder); err != nil {
		return err
	}
	return collectLifecycleEvents(cfg, encoder)
}

func collectSyncEvent(cfg config, encoder *json.Encoder) (retErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), eventTimeout)
	defer cancel()

	object, err := bpf.LoadBPF(syncObject, nil)
	if err != nil {
		return fmt.Errorf("load %s: %w", syncObject, err)
	}
	defer func() { retErr = errors.Join(retErr, object.Close()) }()

	reader, err := object.EventPipeByName(ctx, eventMap, eventBuffer)
	if err != nil {
		return fmt.Errorf("open %s event pipe: %w", syncObject, err)
	}
	defer func() { retErr = errors.Join(retErr, reader.Close()) }()

	if err := object.AttachWithOptions([]bpf.AttachOption{{
		ProgramName: syncProgram,
		Symbol:      cfg.syncSymbol,
	}}); err != nil {
		return fmt.Errorf("attach %s to %s: %w", syncObject, cfg.syncSymbol, err)
	}

	if _, err := os.ReadFile(cfg.syncNotify); err != nil {
		return fmt.Errorf("read cgroup notification file %q: %w", cfg.syncNotify, err)
	}

	return collectEvents(
		reader,
		eventRequest{
			target:   filepath.Base(filepath.Dir(cfg.syncNotify)),
			source:   "sync",
			expected: map[abi.CgroupCSSOperation]string{abi.CgroupCSSOperationUpdate: "update"},
		},
		encoder,
	)
}

func collectLifecycleEvents(cfg config, encoder *json.Encoder) (retErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), eventTimeout)
	defer cancel()

	object, err := bpf.LoadBPF(eventsObject, nil)
	if err != nil {
		return fmt.Errorf("load %s: %w", eventsObject, err)
	}
	defer func() { retErr = errors.Join(retErr, object.Close()) }()

	reader, err := object.AttachAndEventPipe(ctx, eventMap, eventBuffer)
	if err != nil {
		return fmt.Errorf("attach %s: %w", eventsObject, err)
	}
	defer func() { retErr = errors.Join(retErr, reader.Close()) }()

	if err := os.WriteFile(cfg.readyFile, nil, 0o600); err != nil {
		return fmt.Errorf("create ready file %q: %w", cfg.readyFile, err)
	}

	return collectEvents(
		reader,
		eventRequest{
			target: cfg.eventID,
			source: "events",
			expected: map[abi.CgroupCSSOperation]string{
				abi.CgroupCSSOperationUpdate: "update",
				abi.CgroupCSSOperationRemove: "remove",
			},
		},
		encoder,
	)
}

func collectEvents(
	reader bpf.PerfEventReader,
	request eventRequest,
	encoder *json.Encoder,
) error {
	for len(request.expected) != 0 {
		var event abi.CgroupCSSEvent
		if err := reader.ReadInto(&event); err != nil {
			if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
				continue
			}
			return fmt.Errorf("read %s event for cgroup %q: %w", request.source, request.target, err)
		}

		name := strings.TrimRight(string(event.KnodeName[:]), "\x00")
		operation, expected := request.expected[event.Operation]
		if name != request.target || !expected {
			continue
		}

		record := eventRecord{
			Source:      request.source,
			Operation:   operation,
			KnodeName:   name,
			Cgroup:      fmt.Sprintf("%#x", event.Cgroup),
			CgroupRoot:  event.CgroupRoot,
			CgroupLevel: event.CgroupLevel,
			CSSCount:    cssCount(event.CSS),
		}
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("encode %s event: %w", request.source, err)
		}
		delete(request.expected, event.Operation)
	}

	return nil
}

func cssCount(css [13]uint64) int {
	count := 0
	for _, address := range css {
		if address != 0 {
			count++
		}
	}
	return count
}
