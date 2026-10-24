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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
)

const minimumReclaimDurationNS = 20 * 1000 * 1000

type triggerOutput struct {
	PID         uint32 `json:"pid"`
	TID         uint32 `json:"tid"`
	TriggerComm string `json:"comm"`
	ElapsedNS   uint64 `json:"elapsed_ns"`
}

type reclaimEvent struct {
	PID         uint32
	TID         uint32
	Comm        string
	ContainerID string
}

type esReclaimData struct {
	PID        uint32 `json:"pid"`
	TID        uint32 `json:"tid"`
	Comm       string `json:"comm"`
	DurationNS uint64 `json:"reclaim_duration_ns"`
}

type document = bpfmock.Document[esReclaimData]

func (data esReclaimData) TriggerComm() string {
	return data.Comm
}

type containerMetadata struct {
	Data struct {
		ID  string            `json:"id"`
		CSS map[string]string `json:"cgroup_css"`
	} `json:"data"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mock memory reclaim ES verification:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 5 {
		return fmt.Errorf("usage: %s ES_DOCUMENTS_JSON TRIGGER_DIR CONTAINER_JSON FILTER_THRESHOLD_NS [SCENARIO...]", os.Args[0])
	}
	actualDocuments, err := bpfmock.ReadDocuments[esReclaimData](os.Args[1])
	if err != nil {
		return err
	}
	expectedContainer, err := bpfmock.ReadJSON[containerMetadata](os.Args[3])
	if err != nil {
		return err
	}
	if expectedContainer.Data.ID == "" || expectedContainer.Data.CSS["cpu"] == "" {
		return errors.New("incomplete discovered container identity or CPU CSS")
	}
	filteredThreshold, err := strconv.ParseUint(os.Args[4], 10, 64)
	if err != nil || filteredThreshold == 0 {
		return fmt.Errorf("invalid filter threshold %q", os.Args[4])
	}
	scenarios := os.Args[5:]
	if len(scenarios) == 0 {
		scenarios = []string{"host-main", "report-main", "report-thread", "filtered-main", "filtered-thread"}
	}
	expectedDocuments := 0
	for _, scenario := range scenarios {
		expectedRaw, err := bpfmock.ReadJSON[triggerOutput](filepath.Join(os.Args[2], scenario+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(actualDocuments, scenario, expectedRaw, expectedContainer.Data.ID, filteredThreshold); err != nil {
			return fmt.Errorf("%s: %w", scenario, err)
		}
		if scenario == "report-main" || scenario == "report-thread" {
			expectedDocuments++
		}
	}
	if len(actualDocuments) != expectedDocuments {
		return fmt.Errorf("ES document count: got %d, want %d", len(actualDocuments), expectedDocuments)
	}
	fmt.Println("PASS memory reclaim: entry/return timing, task identity, container attribution, host exclusion, and threshold filtering verified")
	return nil
}

func verifyScenario(actualDocuments []document, scenario string, expectedRaw triggerOutput,
	expectedContainerID string, filteredThreshold uint64,
) error {
	expectedEvent, err := expected(scenario, expectedRaw, expectedContainerID, filteredThreshold)
	if err != nil {
		return err
	}
	actualDocument, err := bpfmock.FindDocumentByTriggerComm(actualDocuments, expectedRaw.TriggerComm)
	if err != nil {
		return err
	}
	actualEvent := actual(actualDocument)
	if expectedEvent == nil {
		if actualEvent != nil {
			return errors.New("unexpected ES document for a rejected scenario")
		}
		return nil
	}
	if actualEvent == nil {
		return errors.New("missing ES document")
	}
	if *actualEvent != *expectedEvent {
		return fmt.Errorf("reclaim event: got %+v, want %+v", actualEvent, expectedEvent)
	}
	if err := actualDocument.ValidateMetadata("memory_reclaim"); err != nil {
		return err
	}
	actualDurationNS := actualDocument.Data.DurationNS
	if actualDurationNS < minimumReclaimDurationNS || actualDurationNS > expectedRaw.ElapsedNS {
		return fmt.Errorf("BPF duration %d outside userspace bounds [%d, %d]",
			actualDurationNS, minimumReclaimDurationNS, expectedRaw.ElapsedNS)
	}
	return nil
}

func expected(scenario string, expectedRaw triggerOutput,
	expectedContainerID string, filteredThreshold uint64,
) (*reclaimEvent, error) {
	var threaded, producesEvent, filtered bool
	switch scenario {
	case "host-main":
	case "report-main":
		producesEvent = true
	case "report-thread":
		producesEvent, threaded = true, true
	case "filtered-main":
		filtered = true
	case "filtered-thread":
		filtered, threaded = true, true
	default:
		return nil, fmt.Errorf("unknown scenario %q", scenario)
	}
	if expectedRaw.PID == 0 || expectedRaw.TID == 0 || expectedRaw.TriggerComm == "" ||
		threaded != (expectedRaw.PID != expectedRaw.TID) || expectedRaw.ElapsedNS < minimumReclaimDurationNS {
		return nil, errors.New("invalid userspace task identity or elapsed time")
	}
	if filtered && expectedRaw.ElapsedNS >= filteredThreshold {
		return nil, errors.New("trigger exceeded the configured filtering threshold")
	}
	if !producesEvent {
		return nil, nil
	}
	return &reclaimEvent{
		PID: expectedRaw.PID, TID: expectedRaw.TID,
		Comm: expectedRaw.TriggerComm, ContainerID: expectedContainerID,
	}, nil
}

func actual(actualDocument *document) *reclaimEvent {
	if actualDocument == nil {
		return nil
	}
	return &reclaimEvent{
		PID: actualDocument.Data.PID, TID: actualDocument.Data.TID,
		Comm: actualDocument.Data.Comm, ContainerID: actualDocument.ContainerID,
	}
}
