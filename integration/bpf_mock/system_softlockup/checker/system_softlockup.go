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

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
)

// triggerOutput is recorded in userspace before the mock hook runs.
type triggerOutput struct {
	PID         uint32 `json:"pid"`
	TID         uint32 `json:"tid"`
	CPU         uint32 `json:"cpu"`
	TriggerComm string `json:"comm"`
}

type softlockupEvent struct {
	PID  uint32
	Comm string
	CPU  uint32
}

type esSoftlockupData struct {
	PID   uint32  `json:"pid"`
	Comm  string  `json:"comm"`
	CPU   *uint32 `json:"cpu"`
	Stack *string `json:"cpus_stack"`
}

type document = bpfmock.Document[esSoftlockupData]

func (data esSoftlockupData) TriggerComm() string {
	return data.Comm
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mock softlockup ES verification:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: %s ES_DOCUMENTS_JSON TRIGGER_DIR", os.Args[0])
	}
	actualDocuments, err := bpfmock.ReadDocuments[esSoftlockupData](os.Args[1])
	if err != nil {
		return err
	}
	for _, scenario := range []string{"main", "thread", "other-main", "other-thread"} {
		expectedRaw, err := bpfmock.ReadJSON[triggerOutput](filepath.Join(os.Args[2], scenario+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(actualDocuments, scenario, expectedRaw); err != nil {
			return fmt.Errorf("%s: %w", scenario, err)
		}
	}
	if len(actualDocuments) != 2 {
		return fmt.Errorf("ES document count: got %d, want %d", len(actualDocuments), 2)
	}
	fmt.Printf("PASS softlockup: main, worker, and rejected taint flags verified against ES documents\n")
	return nil
}

func verifyScenario(
	actualDocuments []document,
	scenario string,
	expectedRaw triggerOutput,
) error {
	expectedEvent, err := expected(scenario, expectedRaw)
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
		return fmt.Errorf("softlockup event: got %+v, want %+v", actualEvent, expectedEvent)
	}
	if err := actualDocument.ValidateMetadata("softlockup"); err != nil {
		return err
	}
	if actualDocument.Data.Stack == nil {
		return errors.New("missing CPU stack string")
	}
	return nil
}

func expected(scenario string, expectedRaw triggerOutput) (*softlockupEvent, error) {
	var threaded, rejected bool
	switch scenario {
	case "main":
	case "thread":
		threaded = true
	case "other-main":
		rejected = true
	case "other-thread":
		threaded, rejected = true, true
	default:
		return nil, fmt.Errorf("unknown scenario %q", scenario)
	}
	if expectedRaw.PID == 0 || expectedRaw.TID == 0 || expectedRaw.TriggerComm == "" ||
		threaded != (expectedRaw.PID != expectedRaw.TID) {
		return nil, errors.New("invalid trigger task identity")
	}
	if rejected {
		return nil, nil
	}
	return &softlockupEvent{PID: expectedRaw.PID, Comm: expectedRaw.TriggerComm, CPU: expectedRaw.CPU}, nil
}

func actual(actualDocument *document) (*softlockupEvent, error) {
	if actualDocument == nil {
		return nil, nil
	}
	if actualDocument.Data.CPU == nil {
		return nil, errors.New("missing CPU")
	}
	return &softlockupEvent{
		PID:  actualDocument.Data.PID,
		Comm: actualDocument.Data.Comm,
		CPU:  *actualDocument.Data.CPU,
	}, nil
}
