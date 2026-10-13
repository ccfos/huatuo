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
	"fmt"
	"os"
	"path/filepath"

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
)

type triggerResult struct {
	PID  uint32 `json:"pid"`
	TID  uint32 `json:"tid"`
	CPU  uint32 `json:"cpu"`
	Comm string `json:"comm"`
}

type event struct {
	PID   uint32  `json:"pid"`
	Comm  string  `json:"comm"`
	CPU   *uint32 `json:"cpu"`
	Stack *string `json:"cpus_stack"`
}

type document = bpfmock.Document[event]

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
	documents, err := bpfmock.ReadDocuments[event](os.Args[1])
	if err != nil {
		return err
	}
	if len(documents) != 2 {
		return fmt.Errorf("expected two ES documents, received %d", len(documents))
	}
	matched := make(map[int]bool)
	for _, name := range []string{"main", "thread", "other-main", "other-thread"} {
		task, err := bpfmock.ReadJSON[triggerResult](filepath.Join(os.Args[2], name+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(documents, name, task, matched); err != nil {
			return err
		}
	}
	if len(matched) != len(documents) {
		return fmt.Errorf("unexpected ES documents: matched %d of %d", len(matched), len(documents))
	}
	fmt.Printf("PASS softlockup: main, worker, and rejected taint flags verified against ES documents\n")
	return nil
}

func verifyScenario(documents []document, name string, task triggerResult, matched map[int]bool) error {
	if task.PID == 0 || task.TID == 0 || task.Comm == "" ||
		((name == "thread" || name == "other-thread") && task.PID == task.TID) ||
		((name == "main" || name == "other-main") && task.PID != task.TID) {
		return fmt.Errorf("%s: invalid trigger task identity", name)
	}
	count := 0
	for i := range documents {
		doc := &documents[i]
		if doc.Data.PID != task.PID {
			continue
		}
		if name == "other-main" || name == "other-thread" {
			return fmt.Errorf("%s: rejected taint flag produced an ES document", name)
		}
		count++
		if err := doc.ValidateMetadata("softlockup"); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if matched[i] {
			return fmt.Errorf("%s: ES document already matched another scenario", name)
		}
		if doc.Data.Comm != task.Comm {
			return fmt.Errorf("%s: command name: got %q, want %q", name, doc.Data.Comm, task.Comm)
		}
		if doc.Data.Stack == nil {
			return fmt.Errorf("%s: missing CPU stack string", name)
		}
		if doc.Data.CPU == nil {
			return fmt.Errorf("%s: missing CPU", name)
		}
		if *doc.Data.CPU != task.CPU {
			return fmt.Errorf("%s: CPU: got %d, want %d", name, *doc.Data.CPU, task.CPU)
		}
		matched[i] = true
	}
	if name != "other-main" && name != "other-thread" && count != 1 {
		return fmt.Errorf("%s: expected one ES document, received %d", name, count)
	}
	return nil
}
