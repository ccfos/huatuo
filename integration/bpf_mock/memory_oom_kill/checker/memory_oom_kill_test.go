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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyESDocuments(t *testing.T) {
	// This document has only production JSON fields, without raw page counts.
	const body = `{
 "hostname":"node", "region":"test", "tracer_name":"memory_oom_kill",
 "tracer_type":"event", "tracer_id":"event-id",
 "observed_timestamp":"2026-10-12T00:00:00Z", "uploaded_timestamp":"2026-10-12T00:00:01Z",
 "tracer_data":{
  "trigger":{"pid":42,"comm":"oom-mock","memory_cgroup_css_addr":"0xffff000000000001"},
  "victim":{"pid":42,"comm":"oom-mock","memory_cgroup_css_addr":"0xffff000000000001"},
  "memory_snapshot":{"host_meminfo":{"MemTotal":4096}}
 }}`
	var validDocument document
	if err := json.Unmarshal([]byte(body), &validDocument); err != nil {
		t.Fatal(err)
	}
	expectedRaw := triggerOutput{
		TriggerComm: "oom-mock", TriggerPID: 42, VictimPID: 42,
		TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001,
	}
	for _, tc := range []struct {
		name        string
		change      func(*document)
		count       int
		expectError bool
	}{
		{name: "published fields", count: 1},
		{name: "missing", count: 0, expectError: true},
		{name: "duplicate", count: 2, expectError: true},
		{name: "wrong victim", count: 1, change: func(actualDocument *document) { (*actualDocument.Data.Victim.PID)++ }, expectError: true},
		{name: "wrong CSS", count: 1, change: func(actualDocument *document) { *actualDocument.Data.Victim.CSS = "0xffff000000000002" }, expectError: true},
		{name: "wrong command", count: 1, change: func(actualDocument *document) { *actualDocument.Data.Trigger.Comm = "wrong" }, expectError: true},
		{name: "unexpected task", count: 1, change: func(actualDocument *document) { (*actualDocument.Data.Trigger.PID)++ }, expectError: true},
		{name: "wrong tracer", count: 1, change: func(actualDocument *document) { actualDocument.TracerName = "softlockup" }, expectError: true},
		{name: "missing timestamp", count: 1, change: func(actualDocument *document) { actualDocument.Observed = "" }, expectError: true},
		{name: "missing snapshot", count: 1, change: func(actualDocument *document) { actualDocument.Data.Snapshot.Host = nil }, expectError: true},
		{name: "host document container ID", count: 1, change: func(actualDocument *document) { actualDocument.ContainerID = "unexpected" }, expectError: true},
		{name: "host trigger container ID", count: 1, change: func(actualDocument *document) { actualDocument.Data.Trigger.ContainerID = "unexpected" }, expectError: true},
		{name: "host victim container ID", count: 1, change: func(actualDocument *document) { actualDocument.Data.Victim.ContainerID = "unexpected" }, expectError: true},
		{name: "host trigger container hostname", count: 1, change: func(actualDocument *document) { actualDocument.Data.Trigger.Hostname = "unexpected" }, expectError: true},
		{name: "host victim container hostname", count: 1, change: func(actualDocument *document) { actualDocument.Data.Victim.Hostname = "unexpected" }, expectError: true},
		{name: "host trigger cgroup snapshot", count: 1, change: func(actualDocument *document) { actualDocument.Data.Trigger.Cgroup = &cgroupSnapshot{} }, expectError: true},
		{name: "host victim cgroup snapshot", count: 1, change: func(actualDocument *document) { actualDocument.Data.Victim.Cgroup = &cgroupSnapshot{} }, expectError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actualDocument document
			if err := json.Unmarshal([]byte(body), &actualDocument); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&actualDocument)
			}
			actualDocuments := make([]document, tc.count)
			for i := range actualDocuments {
				actualDocuments[i] = actualDocument
			}
			err := verifyScenario(actualDocuments, "same-task", expectedRaw, nil)
			if (err != nil) != tc.expectError {
				t.Fatalf("verify error=%v, want error=%v", err, tc.expectError)
			}
		})
	}
	if err := verifyScenario(nil, "null-control", expectedRaw, nil); err != nil {
		t.Fatal(err)
	}
	if err := verifyScenario([]document{validDocument}, "null-control", expectedRaw, nil); err == nil {
		t.Fatal("null control emitted a document")
	}
	t.Run("reused PID", func(t *testing.T) {
		var secondDocument document
		if err := json.Unmarshal([]byte(body), &secondDocument); err != nil {
			t.Fatal(err)
		}
		secondExpectedRaw := expectedRaw
		secondExpectedRaw.TriggerComm = "second-trigger"
		*secondDocument.Data.Trigger.Comm = secondExpectedRaw.TriggerComm
		*secondDocument.Data.Victim.Comm = secondExpectedRaw.TriggerComm
		documents := []document{validDocument, secondDocument}
		if err := verifyScenario(documents, "same-task", expectedRaw, nil); err != nil {
			t.Fatal(err)
		}
		if err := verifyScenario(documents, "memcg", secondExpectedRaw, nil); err != nil {
			t.Fatal(err)
		}
		secondExpectedRaw.TriggerComm = "rejected"
		if err := verifyScenario(documents, "null-control", secondExpectedRaw, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExpectedTaskIdentities(t *testing.T) {
	for _, name := range []string{"same-task", "other-task", "memcg", "other-task-memcg", "pointer-chain", "null-victim"} {
		t.Run(name, func(t *testing.T) {
			expectedRaw := triggerOutput{TriggerComm: "oom-mock", TriggerPID: 42, VictimPID: 42, TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001}
			expectedVictim := taskIdentity{PID: 42, Comm: "oom-mock", CSS: "0xffff000000000001"}
			switch name {
			case "other-task", "other-task-memcg", "pointer-chain":
				expectedRaw.VictimPID = 43
				expectedVictim.PID = 43
				expectedVictim.Comm = "oom-mock-victim"
				if name == "pointer-chain" {
					expectedRaw.VictimPID, expectedVictim.PID = 4242, 4242
					expectedRaw.VictimCSS = 0xffff000000000002
					expectedVictim.CSS = "0xffff000000000002"
				}
			case "null-victim":
				expectedRaw.VictimPID = 0
				expectedRaw.VictimCSS = 0
				expectedVictim = taskIdentity{}
			}
			builtEvent, err := expected(name, expectedRaw)
			if err != nil {
				t.Fatal(err)
			}
			if builtEvent.Victim != expectedVictim || builtEvent.Trigger != (taskIdentity{PID: 42, Comm: "oom-mock", CSS: "0xffff000000000001"}) {
				t.Fatalf("unexpected task identities: %+v", builtEvent)
			}
		})
	}
}

func TestContainerEvents(t *testing.T) {
	const body = `{
 "hostname":"node", "region":"test", "tracer_name":"memory_oom_kill",
 "tracer_type":"event", "tracer_id":"id", "container_id":"container-id",
 "observed_timestamp":"2026-10-13T00:00:00Z", "uploaded_timestamp":"2026-10-13T00:00:01Z",
 "tracer_data":{
  "trigger":{"pid":42,"comm":"oom-mock","memory_cgroup_css_addr":"0xffff000000000001",
   "container_id":"container-id","container_hostname":"oom-trigger",
   "cgroup":{"path":"/pods/container-id","max":67108864,"current":4096,"stat":{"anon":4096}}},
  "victim":{"pid":42,"comm":"oom-mock","memory_cgroup_css_addr":"0xffff000000000001",
   "container_id":"container-id","container_hostname":"oom-trigger",
   "cgroup":{"path":"/pods/container-id","max":67108864,"current":4096,"stat":{"anon":4096}}},
  "memory_snapshot":{"host_meminfo":{"MemTotal":4096}}
 }}`
	var expectedContainer containerMetadata
	expectedContainer.Data.ID = "container-id"
	expectedContainer.Data.Hostname = "oom-trigger"
	expectedContainer.Data.CSS = map[string]string{"memory": "0xffff000000000001"}
	expectedRaw := triggerOutput{
		TriggerComm: "oom-mock", TriggerPID: 42, VictimPID: 42,
		TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001,
	}
	for _, tc := range []struct {
		name   string
		change func(*document)
	}{
		{name: "valid"},
		{name: "document ID", change: func(actualDocument *document) { actualDocument.ContainerID = "wrong" }},
		{name: "trigger ID", change: func(actualDocument *document) { actualDocument.Data.Trigger.ContainerID = "wrong" }},
		{name: "victim ID", change: func(actualDocument *document) { actualDocument.Data.Victim.ContainerID = "wrong" }},
		{name: "hostname", change: func(actualDocument *document) { actualDocument.Data.Victim.Hostname = "wrong" }},
		{name: "CSS", change: func(actualDocument *document) { *actualDocument.Data.Trigger.CSS = "wrong" }},
		{name: "missing snapshot", change: func(actualDocument *document) { actualDocument.Data.Trigger.Cgroup = nil }},
		{name: "path", change: func(actualDocument *document) { actualDocument.Data.Victim.Cgroup.Path = "/other" }},
		{name: "limit", change: func(actualDocument *document) { actualDocument.Data.Victim.Cgroup.Max++ }},
		{name: "usage", change: func(actualDocument *document) { actualDocument.Data.Trigger.Cgroup.Current = 0 }},
		{name: "statistics", change: func(actualDocument *document) { actualDocument.Data.Trigger.Cgroup.Stat = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actualDocument document
			if err := json.Unmarshal([]byte(body), &actualDocument); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&actualDocument)
			}
			err := verifyScenario([]document{actualDocument}, "same-task", expectedRaw, &expectedContainer)
			if (err != nil) != (tc.change != nil) {
				t.Fatalf("unexpected verification result: %v", err)
			}
		})
	}
	var actualDocument document
	if err := json.Unmarshal([]byte(body), &actualDocument); err != nil {
		t.Fatal(err)
	}
	actualDocument.ContainerID = ""
	zero := uint32(0)
	empty := ""
	actualDocument.Data.Victim = &esTask{PID: &zero, Comm: &empty, CSS: &empty}
	expectedRaw.VictimPID, expectedRaw.VictimCSS = 0, 0
	if err := verifyScenario([]document{actualDocument}, "null-victim", expectedRaw, &expectedContainer); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*document){
		func(actualDocument *document) { actualDocument.ContainerID = "container-id" },
		func(actualDocument *document) { actualDocument.Data.Victim.ContainerID = "container-id" },
		func(actualDocument *document) { actualDocument.Data.Victim.Hostname = "oom-trigger" },
		func(actualDocument *document) { actualDocument.Data.Victim.Cgroup = &cgroupSnapshot{} },
	} {
		invalidDocument := actualDocument
		victimCopy := *actualDocument.Data.Victim
		invalidDocument.Data.Victim = &victimCopy
		change(&invalidDocument)
		if err := verifyScenario([]document{invalidDocument}, "null-victim", expectedRaw, &expectedContainer); err == nil {
			t.Fatal("null victim accepted container metadata")
		}
	}
}

func TestRequiredTaskFields(t *testing.T) {
	const body = `{
 "hostname":"node", "region":"test", "tracer_name":"memory_oom_kill",
 "tracer_type":"event", "tracer_id":"id",
 "observed_timestamp":"2026-10-13T00:00:00Z", "uploaded_timestamp":"2026-10-13T00:00:01Z",
 "tracer_data":{
  "trigger":{"pid":42,"comm":"oom-mock","memory_cgroup_css_addr":"0xffff000000000001"},
  "victim":{"pid":0,"comm":"","memory_cgroup_css_addr":""},
  "memory_snapshot":{"host_meminfo":{"MemTotal":4096}}
 }}`
	expectedRaw := triggerOutput{TriggerComm: "oom-mock", TriggerPID: 42, TriggerCSS: 0xffff000000000001}
	var validDocument document
	if err := json.Unmarshal([]byte(body), &validDocument); err != nil {
		t.Fatal(err)
	}
	if err := verifyScenario([]document{validDocument}, "null-victim", expectedRaw, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"trigger", "victim"} {
		for _, field := range []string{"object", "pid", "comm", "memory_cgroup_css_addr"} {
			for _, mode := range []string{"missing", "null"} {
				t.Run(name+"/"+field+"/"+mode, func(t *testing.T) {
					var input map[string]any
					if err := json.Unmarshal([]byte(body), &input); err != nil {
						t.Fatal(err)
					}
					data := input["tracer_data"].(map[string]any)
					target, key := data, name
					if field != "object" {
						target, key = data[name].(map[string]any), field
					}
					if mode == "null" {
						target[key] = nil
					} else {
						delete(target, key)
					}
					encoded, err := json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
					var actualDocument document
					if err := json.Unmarshal(encoded, &actualDocument); err != nil {
						t.Fatal(err)
					}
					if _, err := actual(&actualDocument); err == nil {
						t.Fatal("actual event accepted missing or null task data")
					}
					if err := verifyScenario([]document{actualDocument}, "null-victim", expectedRaw, nil); err == nil {
						t.Fatal("missing or null task data accepted")
					}
				})
			}
		}
	}
}

func TestNullControlPreparedIdentity(t *testing.T) {
	// Preparation observes chosen=current; running the scenario passes NULL to the hook.
	expectedRaw := triggerOutput{
		TriggerComm: "oom-mock", TriggerPID: 42, VictimPID: 42,
		TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001,
	}
	builtEvent, err := expected("null-control", expectedRaw)
	if err != nil || builtEvent != nil {
		t.Fatalf("null control: event=%+v, error=%v", builtEvent, err)
	}
}

func TestCheckerArguments(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"events.json":       "",
		"null-control.json": `{"trigger_comm":"mock","trigger_pid":42,"victim_pid":42,"trigger_css":4096,"victim_css":4096}`,
		"container.json":    `{"data":{"id":"container","hostname":"pod","cgroup_css":{"memory":"0x1000"}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previousArgs := os.Args
	t.Cleanup(func() { os.Args = previousArgs })
	for _, container := range []bool{false, true} {
		args := []string{"oom-checker", filepath.Join(dir, "events.json")}
		if container {
			args = append(args, "-container", filepath.Join(dir, "container.json"))
		}
		os.Args = append(args, dir, "null-control")
		if err := run(); err != nil {
			t.Fatalf("container=%t: %v", container, err)
		}
		if err := os.WriteFile(os.Args[1], []byte(`{"tracer_data":{"trigger":{"comm":"unrelated"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(); err == nil {
			t.Fatal("unexpected document accepted")
		}
		if err := os.WriteFile(os.Args[1], nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
