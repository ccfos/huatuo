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

type triggerResult struct {
	Comm       string `json:"comm"`
	TriggerPID uint32 `json:"trigger_pid"`
	VictimPID  uint32 `json:"victim_pid"`
	TriggerCSS uint64 `json:"trigger_css"`
	VictimCSS  uint64 `json:"victim_css"`
}

type actor struct {
	PID  uint32 `json:"pid"`
	Comm string `json:"comm"`
	CSS  string `json:"memory_cgroup_css_addr"`
}

type oomEvent struct {
	Trigger actor `json:"trigger"`
	Victim  actor `json:"victim"`
}

type cgroupSnapshot struct {
	Path    string            `json:"path"`
	Max     uint64            `json:"max"`
	Current uint64            `json:"current"`
	Stat    map[string]uint64 `json:"stat"`
}

type containerActor struct {
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

type oomData struct {
	Trigger  *containerActor `json:"trigger"`
	Victim   *containerActor `json:"victim"`
	Snapshot struct {
		Host map[string]uint64 `json:"host_meminfo"`
	} `json:"memory_snapshot"`
}

type document = bpfmock.Document[oomData]

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mock OOM ES verification:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	containerPath := flags.String("container", "", "expected container identity and memory CSS JSON")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := flags.Args()
	if len(args) < 3 {
		return fmt.Errorf("usage: %s [-container METADATA_JSON] ES_DOCUMENTS_JSON TRIGGER_DIR SCENARIO...", os.Args[0])
	}
	var container *containerMetadata
	if *containerPath != "" {
		metadata, err := bpfmock.ReadJSON[containerMetadata](*containerPath)
		if err != nil {
			return err
		}
		if metadata.Data.ID == "" || metadata.Data.Hostname == "" || metadata.Data.CSS["memory"] == "" {
			return errors.New("incomplete discovered container metadata")
		}
		container = &metadata
	}
	documents, err := bpfmock.ReadDocuments[oomData](args[0])
	if err != nil {
		return err
	}
	matched := make(map[int]bool)
	for _, name := range args[2:] {
		task, err := bpfmock.ReadJSON[triggerResult](filepath.Join(args[1], name+".json"))
		if err != nil {
			return err
		}
		if err := verifyScenario(documents, name, task, matched, container); err != nil {
			return err
		}
	}
	if len(matched) != len(documents) {
		return fmt.Errorf("unexpected ES documents: matched %d of %d", len(matched), len(documents))
	}
	fmt.Printf("PASS OOM: %d scenarios verified against %d ES documents\n", len(args[2:]), len(documents))
	return nil
}

func verifyScenario(documents []document, name string, task triggerResult, matched map[int]bool, container *containerMetadata) error {
	want, err := expected(name, task)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	count := 0
	for i := range documents {
		doc := &documents[i]
		trigger, err := doc.Data.Trigger.identity()
		if err != nil {
			return fmt.Errorf("%s: trigger: %w", name, err)
		}
		victim, err := doc.Data.Victim.identity()
		if err != nil {
			return fmt.Errorf("%s: victim: %w", name, err)
		}
		if trigger.PID != task.TriggerPID {
			continue
		}
		count++
		if want == nil {
			return fmt.Errorf("%s: unexpected ES document", name)
		}
		if matched[i] {
			return fmt.Errorf("%s: ES document already matched another scenario", name)
		}
		if trigger != want.Trigger || victim != want.Victim {
			return fmt.Errorf("%s: actor mismatch: got trigger %+v, victim %+v; want %+v", name, trigger, victim, want)
		}
		if err := doc.ValidateMetadata("memory_oom_kill"); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if doc.Data.Snapshot.Host["MemTotal"] == 0 {
			return fmt.Errorf("%s: missing ES host memory snapshot", name)
		}
		if err := verifyContainer(doc, container); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		matched[i] = true
	}
	if want != nil && count != 1 {
		return fmt.Errorf("%s: expected one ES document, received %d", name, count)
	}
	return nil
}

func expected(name string, task triggerResult) (*oomEvent, error) {
	if task.TriggerPID == 0 || task.TriggerCSS == 0 || task.Comm == "" {
		return nil, errors.New("fixture returned an invalid trigger identity")
	}
	if name == "null-victim" {
		if task.VictimPID != 0 || task.VictimCSS != 0 {
			return nil, errors.New("fixture did not clear the victim")
		}
	} else if task.VictimPID == 0 || task.VictimCSS == 0 {
		return nil, errors.New("fixture returned an invalid victim identity")
	}
	if name == "null-control" {
		return nil, nil
	}
	victimComm := task.Comm
	switch name {
	case "other-task", "other-task-memcg", "pointer-chain":
		victimComm = "oom-mock-victim"
		if task.TriggerPID == task.VictimPID {
			return nil, errors.New("fixture did not create a distinct victim")
		}
	case "null-victim":
		victimComm = ""
	case "same-task", "memcg":
		if task.TriggerPID != task.VictimPID || task.TriggerCSS != task.VictimCSS {
			return nil, errors.New("fixture did not use the trigger as victim")
		}
	default:
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
	if name == "pointer-chain" && task.TriggerCSS == task.VictimCSS {
		return nil, errors.New("fixture did not create distinct cgroup pointer chains")
	}
	return &oomEvent{
		Trigger: actor{PID: task.TriggerPID, Comm: task.Comm, CSS: kernaddr.Format(task.TriggerCSS)},
		Victim:  actor{PID: task.VictimPID, Comm: victimComm, CSS: kernaddr.Format(task.VictimCSS)},
	}, nil
}

func verifyContainer(doc *document, container *containerMetadata) error {
	if err := verifyContainerActor(doc.Data.Trigger, container); err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	victimContainer := container
	if *doc.Data.Victim.PID == 0 {
		victimContainer = nil
	}
	if err := verifyContainerActor(doc.Data.Victim, victimContainer); err != nil {
		return fmt.Errorf("victim: %w", err)
	}
	wantID := ""
	if victimContainer != nil {
		wantID = victimContainer.Data.ID
	}
	if doc.ContainerID != wantID {
		return fmt.Errorf("document container ID: got %q, want %q", doc.ContainerID, wantID)
	}
	return nil
}

func verifyContainerActor(a *containerActor, container *containerMetadata) error {
	if container == nil {
		if a.ContainerID != "" || a.Hostname != "" || a.Cgroup != nil {
			return errors.New("unexpected container metadata")
		}
		return nil
	}
	if a.ContainerID != container.Data.ID {
		return fmt.Errorf("container ID: got %q, want %q", a.ContainerID, container.Data.ID)
	}
	if a.Hostname != container.Data.Hostname {
		return fmt.Errorf("container hostname: got %q, want %q", a.Hostname, container.Data.Hostname)
	}
	if *a.CSS != container.Data.CSS["memory"] {
		return fmt.Errorf("memory CSS: got %q, want %q", *a.CSS, container.Data.CSS["memory"])
	}
	if a.Cgroup == nil {
		return errors.New("missing cgroup snapshot")
	}
	if !strings.Contains(a.Cgroup.Path, container.Data.ID) {
		return fmt.Errorf("cgroup path %q does not contain container ID %q", a.Cgroup.Path, container.Data.ID)
	}
	const podMemoryLimit = 64 * 1024 * 1024
	if a.Cgroup.Max != podMemoryLimit {
		return fmt.Errorf("cgroup memory limit: got %d, want %d", a.Cgroup.Max, podMemoryLimit)
	}
	if a.Cgroup.Current == 0 || len(a.Cgroup.Stat) == 0 {
		return errors.New("missing cgroup memory usage or statistics")
	}
	return nil
}

func (a *containerActor) identity() (actor, error) {
	if a == nil {
		return actor{}, errors.New("missing actor object")
	}
	if a.PID == nil || a.Comm == nil || a.CSS == nil {
		return actor{}, errors.New("missing actor pid, comm, or memory_cgroup_css_addr")
	}
	return actor{PID: *a.PID, Comm: *a.Comm, CSS: *a.CSS}, nil
}
