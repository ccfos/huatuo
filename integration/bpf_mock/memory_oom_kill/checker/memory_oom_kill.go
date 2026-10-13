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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
)

// triggerOutput combines userspace process IDs with API or kernel CSS observations.
type triggerOutput struct {
	TriggerComm string `json:"trigger_comm"`
	TriggerPID  uint32 `json:"trigger_pid"`
	VictimPID   uint32 `json:"victim_pid"`
	TriggerCSS  uint64 `json:"trigger_css"`
	VictimCSS   uint64 `json:"victim_css"`
}

type taskIdentity struct {
	PID  uint32 `json:"pid"`
	Comm string `json:"comm"`
	CSS  string `json:"memory_cgroup_css_addr"`
}

type oomEvent struct {
	Trigger taskIdentity `json:"trigger"`
	Victim  taskIdentity `json:"victim"`
}

type cgroupSnapshot struct {
	Path    string            `json:"path"`
	Max     uint64            `json:"max"`
	Current uint64            `json:"current"`
	Stat    map[string]uint64 `json:"stat"`
}

type esTask struct {
	PID         *uint32         `json:"pid"`
	Comm        *string         `json:"comm"`
	CSS         *string         `json:"memory_cgroup_css_addr"`
	ContainerID string          `json:"container_id"`
	Hostname    string          `json:"container_hostname"`
	Cgroup      *cgroupSnapshot `json:"cgroup"`
}

type containerMetadata struct {
	Data struct {
		ID       string            `json:"id"`
		Hostname string            `json:"hostname"`
		CSS      map[string]string `json:"cgroup_css"`
	} `json:"data"`
}

type esOOMData struct {
	Trigger  *esTask `json:"trigger"`
	Victim   *esTask `json:"victim"`
	Snapshot struct {
		Host map[string]uint64 `json:"host_meminfo"`
	} `json:"memory_snapshot"`
}

type document = bpfmock.Document[esOOMData]

func (data esOOMData) TriggerComm() string {
	if data.Trigger == nil || data.Trigger.Comm == nil {
		return ""
	}
	return *data.Trigger.Comm
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mock OOM ES verification:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: %s ES_DOCUMENTS_JSON [-container METADATA_JSON] TRIGGER_DIR SCENARIO...", os.Args[0])
	}
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	expectedContainerPath := flags.String("container", "", "expected container identity and memory CSS JSON")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	args := flags.Args()
	if len(args) < 2 {
		return fmt.Errorf("usage: %s ES_DOCUMENTS_JSON [-container METADATA_JSON] TRIGGER_DIR SCENARIO...", os.Args[0])
	}
	var expectedContainer *containerMetadata
	if *expectedContainerPath != "" {
		expectedMetadata, err := bpfmock.ReadJSON[containerMetadata](*expectedContainerPath)
		if err != nil {
			return err
		}
		if expectedMetadata.Data.ID == "" || expectedMetadata.Data.Hostname == "" || expectedMetadata.Data.CSS["memory"] == "" {
			return errors.New("incomplete discovered container metadata")
		}
		expectedContainer = &expectedMetadata
	}
	actualDocuments, err := bpfmock.ReadDocuments[esOOMData](os.Args[1])
	if err != nil {
		return err
	}
	expectedDocuments := 0
	for _, scenario := range args[1:] {
		expectedRaw, err := bpfmock.ReadJSON[triggerOutput](filepath.Join(args[0], scenario+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(actualDocuments, scenario, expectedRaw, expectedContainer); err != nil {
			return fmt.Errorf("%s: %w", scenario, err)
		}
		if scenario != "null-control" {
			expectedDocuments++
		}
	}
	if len(actualDocuments) != expectedDocuments {
		return fmt.Errorf("ES document count: got %d, want %d", len(actualDocuments), expectedDocuments)
	}
	fmt.Printf("PASS OOM: %d scenarios verified against %d ES documents\n", len(args[1:]), len(actualDocuments))
	return nil
}

func verifyScenario(
	actualDocuments []document,
	scenario string,
	expectedRaw triggerOutput,
	expectedContainer *containerMetadata,
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
		return fmt.Errorf("task identity mismatch: got %+v, want %+v", actualEvent, expectedEvent)
	}
	if err := actualDocument.ValidateMetadata("memory_oom_kill"); err != nil {
		return err
	}
	if actualDocument.Data.Snapshot.Host["MemTotal"] == 0 {
		return errors.New("missing ES host memory snapshot")
	}
	if err := verifyActualTaskContainerInfo(actualDocument.Data.Trigger, expectedContainer); err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	expectedVictimContainer := expectedContainer
	if expectedEvent.Victim.PID == 0 {
		expectedVictimContainer = nil
	}
	if err := verifyActualTaskContainerInfo(actualDocument.Data.Victim, expectedVictimContainer); err != nil {
		return fmt.Errorf("victim: %w", err)
	}
	expectedContainerID := ""
	if expectedVictimContainer != nil {
		expectedContainerID = expectedVictimContainer.Data.ID
	}
	if actualDocument.ContainerID != expectedContainerID {
		return fmt.Errorf("document container ID: got %q, want %q", actualDocument.ContainerID, expectedContainerID)
	}
	return nil
}

func expected(scenario string, expectedRaw triggerOutput) (*oomEvent, error) {
	if expectedRaw.TriggerPID == 0 || expectedRaw.TriggerCSS == 0 || expectedRaw.TriggerComm == "" {
		return nil, errors.New("fixture preparation reported an invalid trigger identity")
	}
	if scenario == "null-victim" {
		if expectedRaw.VictimPID != 0 || expectedRaw.VictimCSS != 0 {
			return nil, errors.New("fixture did not clear the victim")
		}
	} else if expectedRaw.VictimPID == 0 || expectedRaw.VictimCSS == 0 {
		return nil, errors.New("fixture preparation reported an invalid victim identity")
	}
	if scenario == "null-control" {
		return nil, nil
	}
	expectedVictimComm := expectedRaw.TriggerComm
	switch scenario {
	case "other-task", "other-task-memcg":
		expectedVictimComm = "oom-mock-victim"
		if expectedRaw.TriggerPID == expectedRaw.VictimPID {
			return nil, errors.New("fixture did not create a distinct victim")
		}
	case "pointer-chain":
		expectedVictimComm = "oom-mock-victim"
		if expectedRaw.VictimPID != 4242 ||
			expectedRaw.TriggerPID == expectedRaw.VictimPID ||
			expectedRaw.TriggerCSS == expectedRaw.VictimCSS {
			return nil, errors.New("invalid synthetic victim identity or cgroup pointer chain")
		}
	case "null-victim":
		expectedVictimComm = ""
	case "same-task", "memcg":
		if expectedRaw.TriggerPID != expectedRaw.VictimPID ||
			expectedRaw.TriggerCSS != expectedRaw.VictimCSS {
			return nil, errors.New("fixture did not use the trigger as victim")
		}
	default:
		return nil, fmt.Errorf("unknown scenario %q", scenario)
	}
	return &oomEvent{
		Trigger: taskIdentity{
			PID:  expectedRaw.TriggerPID,
			Comm: expectedRaw.TriggerComm,
			CSS:  kernaddr.Format(expectedRaw.TriggerCSS),
		},
		Victim: taskIdentity{
			PID:  expectedRaw.VictimPID,
			Comm: expectedVictimComm,
			CSS:  kernaddr.Format(expectedRaw.VictimCSS),
		},
	}, nil
}

func actual(actualDocument *document) (*oomEvent, error) {
	if actualDocument == nil {
		return nil, nil
	}
	trigger, err := actualDocument.Data.Trigger.identity()
	if err != nil {
		return nil, fmt.Errorf("trigger: %w", err)
	}
	victim, err := actualDocument.Data.Victim.identity()
	if err != nil {
		return nil, fmt.Errorf("victim: %w", err)
	}
	return &oomEvent{Trigger: trigger, Victim: victim}, nil
}

func verifyActualTaskContainerInfo(actualTask *esTask, expectedContainer *containerMetadata) error {
	if expectedContainer == nil {
		if actualTask.ContainerID != "" || actualTask.Hostname != "" || actualTask.Cgroup != nil {
			return errors.New("unexpected container metadata")
		}
		return nil
	}
	if actualTask.ContainerID != expectedContainer.Data.ID {
		return fmt.Errorf("container ID: got %q, want %q", actualTask.ContainerID, expectedContainer.Data.ID)
	}
	if actualTask.Hostname != expectedContainer.Data.Hostname {
		return fmt.Errorf("container hostname: got %q, want %q", actualTask.Hostname, expectedContainer.Data.Hostname)
	}
	if *actualTask.CSS != expectedContainer.Data.CSS["memory"] {
		return fmt.Errorf("memory CSS: got %q, want %q", *actualTask.CSS, expectedContainer.Data.CSS["memory"])
	}
	if actualTask.Cgroup == nil {
		return errors.New("missing cgroup snapshot")
	}
	if !strings.Contains(actualTask.Cgroup.Path, expectedContainer.Data.ID) {
		return fmt.Errorf("cgroup path %q does not contain container ID %q", actualTask.Cgroup.Path, expectedContainer.Data.ID)
	}
	const podMemoryLimit = 64 * 1024 * 1024
	if actualTask.Cgroup.Max != podMemoryLimit {
		return fmt.Errorf("cgroup memory limit: got %d, want %d", actualTask.Cgroup.Max, podMemoryLimit)
	}
	if actualTask.Cgroup.Current == 0 || len(actualTask.Cgroup.Stat) == 0 {
		return errors.New("missing cgroup memory usage or statistics")
	}
	return nil
}

func (actualTask *esTask) identity() (taskIdentity, error) {
	if actualTask == nil {
		return taskIdentity{}, errors.New("missing task object")
	}
	if actualTask.PID == nil || actualTask.Comm == nil || actualTask.CSS == nil {
		return taskIdentity{}, errors.New("missing task pid, comm, or memory_cgroup_css_addr")
	}
	return taskIdentity{PID: *actualTask.PID, Comm: *actualTask.Comm, CSS: *actualTask.CSS}, nil
}
