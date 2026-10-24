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

// triggerOutput is recorded in userspace before the mock hook runs.
type triggerOutput struct {
	PID         uint32 `json:"pid"`
	TID         uint32 `json:"tid"`
	CPU         uint32 `json:"cpu"`
	TriggerComm string `json:"comm"`
}

type hungtaskEvent struct {
	TID     uint32
	Comm    string
	Timeout int
}

type esHungtaskData struct {
	TID     uint32  `json:"tid"`
	Comm    string  `json:"comm"`
	Timeout *int    `json:"hung_task_timeout_secs"`
	Stack   *string `json:"cpus_stack"`
	Blocked *string `json:"blocked_processes_stack"`
}

type document = bpfmock.Document[esHungtaskData]

func (data esHungtaskData) TriggerComm() string {
	return data.Comm
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mock hungtask ES verification:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 4 {
		return fmt.Errorf("usage: %s ES_DOCUMENTS_JSON TRIGGER_DIR TIMEOUT_SECONDS", os.Args[0])
	}
	actualDocuments, err := bpfmock.ReadDocuments[esHungtaskData](os.Args[1])
	if err != nil {
		return err
	}
	expectedTimeout, err := strconv.Atoi(os.Args[3])
	if err != nil || expectedTimeout < 0 {
		return fmt.Errorf("invalid hung task timeout %q", os.Args[3])
	}
	for _, scenario := range []string{"main", "thread"} {
		expectedRaw, err := bpfmock.ReadJSON[triggerOutput](filepath.Join(os.Args[2], scenario+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(actualDocuments, scenario, expectedRaw, expectedTimeout); err != nil {
			return fmt.Errorf("%s: %w", scenario, err)
		}
	}
	if len(actualDocuments) != 2 {
		return fmt.Errorf("ES document count: got %d, want %d", len(actualDocuments), 2)
	}
	fmt.Printf("PASS hungtask: main and worker events verified against ES documents\n")
	return nil
}

func verifyScenario(
	actualDocuments []document,
	scenario string,
	expectedRaw triggerOutput,
	expectedTimeout int,
) error {
	expectedEvent, err := expected(scenario, expectedRaw, expectedTimeout)
	if err != nil {
		return err
	}
	actualDocument, err := bpfmock.FindDocumentByTriggerComm(actualDocuments, expectedRaw.TriggerComm)
	if err != nil {
		return err
	}
	actualEvent, err := actual(actualDocument)
	if err != nil {
		return err
	}
	if actualEvent == nil {
		return errors.New("missing ES document")
	}
	if *actualEvent != *expectedEvent {
		return fmt.Errorf("hung task event: got %+v, want %+v", actualEvent, expectedEvent)
	}
	if err := actualDocument.ValidateMetadata("hungtask"); err != nil {
		return err
	}
	if actualDocument.Data.Stack == nil {
		return errors.New("missing CPU stack string")
	}
	if actualDocument.Data.Blocked == nil {
		return errors.New("missing blocked-task stack string")
	}
	return nil
}

func expected(scenario string, expectedRaw triggerOutput, expectedTimeout int) (*hungtaskEvent, error) {
	if scenario != "main" && scenario != "thread" {
		return nil, fmt.Errorf("unknown scenario %q", scenario)
	}
	if expectedRaw.PID == 0 || expectedRaw.TID == 0 || expectedRaw.TriggerComm == "" ||
		(scenario == "thread") != (expectedRaw.PID != expectedRaw.TID) {
		return nil, errors.New("invalid trigger task identity")
	}
	return &hungtaskEvent{TID: expectedRaw.TID, Comm: expectedRaw.TriggerComm, Timeout: expectedTimeout}, nil
}

func actual(actualDocument *document) (*hungtaskEvent, error) {
	if actualDocument == nil {
		return nil, nil
	}
	if actualDocument.Data.Timeout == nil {
		return nil, errors.New("missing hung task timeout")
	}
	return &hungtaskEvent{
		TID:     actualDocument.Data.TID,
		Comm:    actualDocument.Data.Comm,
		Timeout: *actualDocument.Data.Timeout,
	}, nil
}
