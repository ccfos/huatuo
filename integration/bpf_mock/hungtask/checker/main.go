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
	"strconv"

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
)

type triggerResult struct {
	PID  uint32 `json:"pid"`
	TID  uint32 `json:"tid"`
	CPU  uint32 `json:"cpu"`
	Comm string `json:"comm"`
}

type event struct {
	TID     uint32  `json:"tid"`
	Comm    string  `json:"comm"`
	Timeout *int    `json:"hung_task_timeout_secs"`
	Stack   *string `json:"cpus_stack"`
	Blocked *string `json:"blocked_processes_stack"`
}

type document = bpfmock.Document[event]

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
	documents, err := bpfmock.ReadDocuments[event](os.Args[1])
	if err != nil {
		return err
	}
	timeout, err := strconv.Atoi(os.Args[3])
	if err != nil || timeout < 0 {
		return fmt.Errorf("invalid hung task timeout %q", os.Args[3])
	}
	if len(documents) != 2 {
		return fmt.Errorf("expected two ES documents, received %d", len(documents))
	}
	matched := make(map[int]bool)
	for _, name := range []string{"main", "thread"} {
		task, err := bpfmock.ReadJSON[triggerResult](filepath.Join(os.Args[2], name+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(documents, name, task, matched, timeout); err != nil {
			return err
		}
	}
	if len(matched) != len(documents) {
		return fmt.Errorf("unexpected ES documents: matched %d of %d", len(matched), len(documents))
	}
	fmt.Printf("PASS hungtask: main and worker events verified against ES documents\n")
	return nil
}

func verifyScenario(documents []document, name string, task triggerResult, matched map[int]bool, timeout int) error {
	if task.PID == 0 || task.TID == 0 || task.Comm == "" ||
		(name == "thread" && task.PID == task.TID) || (name == "main" && task.PID != task.TID) {
		return fmt.Errorf("%s: invalid trigger task identity", name)
	}
	count := 0
	for i := range documents {
		doc := &documents[i]
		if doc.Data.TID != task.TID {
			continue
		}
		count++
		if err := doc.ValidateMetadata("hungtask"); err != nil {
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
		if doc.Data.Blocked == nil {
			return fmt.Errorf("%s: missing blocked-task stack string", name)
		}
		if doc.Data.Timeout == nil {
			return fmt.Errorf("%s: missing hung task timeout", name)
		}
		if *doc.Data.Timeout != timeout {
			return fmt.Errorf("%s: hung task timeout: got %d, want %d", name, *doc.Data.Timeout, timeout)
		}
		matched[i] = true
	}
	if count != 1 {
		return fmt.Errorf("%s: expected one ES document, received %d", name, count)
	}
	return nil
}
