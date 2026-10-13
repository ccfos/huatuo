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
	var valid document
	if err := json.Unmarshal([]byte(body), &valid); err != nil {
		t.Fatal(err)
	}
	task := triggerResult{
		Comm: "oom-mock", TriggerPID: 42, VictimPID: 42,
		TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001,
	}
	for _, tc := range []struct {
		name      string
		change    func(*document)
		count     int
		wantError bool
	}{
		{name: "published fields", count: 1},
		{name: "missing", count: 0, wantError: true},
		{name: "duplicate", count: 2, wantError: true},
		{name: "wrong victim", count: 1, change: func(d *document) { (*d.Data.Victim.PID)++ }, wantError: true},
		{name: "wrong CSS", count: 1, change: func(d *document) { *d.Data.Victim.CSS = "0xffff000000000002" }, wantError: true},
		{name: "wrong command", count: 1, change: func(d *document) { *d.Data.Trigger.Comm = "wrong" }, wantError: true},
		{name: "unexpected task", count: 1, change: func(d *document) { (*d.Data.Trigger.PID)++ }, wantError: true},
		{name: "wrong tracer", count: 1, change: func(d *document) { d.TracerName = "softlockup" }, wantError: true},
		{name: "missing timestamp", count: 1, change: func(d *document) { d.Observed = "" }, wantError: true},
		{name: "missing snapshot", count: 1, change: func(d *document) { d.Data.Snapshot.Host = nil }, wantError: true},
		{name: "host document container ID", count: 1, change: func(d *document) { d.ContainerID = "unexpected" }, wantError: true},
		{name: "host trigger container ID", count: 1, change: func(d *document) { d.Data.Trigger.ContainerID = "unexpected" }, wantError: true},
		{name: "host victim container ID", count: 1, change: func(d *document) { d.Data.Victim.ContainerID = "unexpected" }, wantError: true},
		{name: "host trigger container hostname", count: 1, change: func(d *document) { d.Data.Trigger.Hostname = "unexpected" }, wantError: true},
		{name: "host victim container hostname", count: 1, change: func(d *document) { d.Data.Victim.Hostname = "unexpected" }, wantError: true},
		{name: "host trigger cgroup snapshot", count: 1, change: func(d *document) { d.Data.Trigger.Cgroup = &cgroupSnapshot{} }, wantError: true},
		{name: "host victim cgroup snapshot", count: 1, change: func(d *document) { d.Data.Victim.Cgroup = &cgroupSnapshot{} }, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc document
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&doc)
			}
			documents := make([]document, tc.count)
			for i := range documents {
				documents[i] = doc
			}
			err := verifyScenario(documents, "same-task", task, make(map[int]bool), nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("verify error=%v, want error=%v", err, tc.wantError)
			}
		})
	}
	matched := make(map[int]bool)
	if err := verifyScenario([]document{valid}, "same-task", task, matched, nil); err != nil {
		t.Fatal(err)
	}
	if !matched[0] {
		t.Fatal("verified document was not marked as matched")
	}
	if err := verifyScenario([]document{valid}, "memcg", task, matched, nil); err == nil {
		t.Fatal("same document matched two scenarios")
	}
	if err := verifyScenario(nil, "null-control", task, make(map[int]bool), nil); err != nil {
		t.Fatal(err)
	}
	if err := verifyScenario([]document{valid}, "null-control", task, make(map[int]bool), nil); err == nil {
		t.Fatal("null control emitted a document")
	}
}

func TestExpectedPublishedActors(t *testing.T) {
	for _, name := range []string{"same-task", "other-task", "memcg", "other-task-memcg", "pointer-chain", "null-victim"} {
		t.Run(name, func(t *testing.T) {
			task := triggerResult{Comm: "oom-mock", TriggerPID: 42, VictimPID: 42, TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001}
			want := actor{PID: 42, Comm: "oom-mock", CSS: "0xffff000000000001"}
			switch name {
			case "other-task", "other-task-memcg", "pointer-chain":
				task.VictimPID = 43
				want.PID = 43
				want.Comm = "oom-mock-victim"
				if name == "pointer-chain" {
					task.VictimCSS = 0xffff000000000002
					want.CSS = "0xffff000000000002"
				}
			case "null-victim":
				task.VictimPID = 0
				task.VictimCSS = 0
				want = actor{}
			}
			got, err := expected(name, task)
			if err != nil {
				t.Fatal(err)
			}
			if got.Victim != want || got.Trigger != (actor{PID: 42, Comm: "oom-mock", CSS: "0xffff000000000001"}) {
				t.Fatalf("unexpected actors: %+v", got)
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
	var container containerMetadata
	container.Data.ID = "container-id"
	container.Data.Hostname = "oom-trigger"
	container.Data.CSS = map[string]string{"memory": "0xffff000000000001"}
	task := triggerResult{
		Comm: "oom-mock", TriggerPID: 42, VictimPID: 42,
		TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001,
	}
	for _, tc := range []struct {
		name   string
		change func(*document)
	}{
		{name: "valid"},
		{name: "document ID", change: func(d *document) { d.ContainerID = "wrong" }},
		{name: "trigger ID", change: func(d *document) { d.Data.Trigger.ContainerID = "wrong" }},
		{name: "victim ID", change: func(d *document) { d.Data.Victim.ContainerID = "wrong" }},
		{name: "hostname", change: func(d *document) { d.Data.Victim.Hostname = "wrong" }},
		{name: "CSS", change: func(d *document) { *d.Data.Trigger.CSS = "wrong" }},
		{name: "missing snapshot", change: func(d *document) { d.Data.Trigger.Cgroup = nil }},
		{name: "path", change: func(d *document) { d.Data.Victim.Cgroup.Path = "/other" }},
		{name: "limit", change: func(d *document) { d.Data.Victim.Cgroup.Max++ }},
		{name: "usage", change: func(d *document) { d.Data.Trigger.Cgroup.Current = 0 }},
		{name: "statistics", change: func(d *document) { d.Data.Trigger.Cgroup.Stat = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc document
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&doc)
			}
			err := verifyScenario([]document{doc}, "same-task", task, make(map[int]bool), &container)
			if (err != nil) != (tc.change != nil) {
				t.Fatalf("unexpected verification result: %v", err)
			}
		})
	}
	var doc document
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	doc.ContainerID = ""
	zero := uint32(0)
	empty := ""
	doc.Data.Victim = &containerActor{PID: &zero, Comm: &empty, CSS: &empty}
	task.VictimPID, task.VictimCSS = 0, 0
	if err := verifyScenario([]document{doc}, "null-victim", task, make(map[int]bool), &container); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*document){
		func(d *document) { d.ContainerID = "container-id" },
		func(d *document) { d.Data.Victim.ContainerID = "container-id" },
		func(d *document) { d.Data.Victim.Hostname = "oom-trigger" },
		func(d *document) { d.Data.Victim.Cgroup = &cgroupSnapshot{} },
	} {
		bad := doc
		victim := *doc.Data.Victim
		bad.Data.Victim = &victim
		change(&bad)
		if err := verifyScenario([]document{bad}, "null-victim", task, make(map[int]bool), &container); err == nil {
			t.Fatal("null victim accepted container metadata")
		}
	}
}

func TestRequiredActorFields(t *testing.T) {
	const body = `{
 "hostname":"node", "region":"test", "tracer_name":"memory_oom_kill",
 "tracer_type":"event", "tracer_id":"id",
 "observed_timestamp":"2026-10-13T00:00:00Z", "uploaded_timestamp":"2026-10-13T00:00:01Z",
 "tracer_data":{
  "trigger":{"pid":42,"comm":"oom-mock","memory_cgroup_css_addr":"0xffff000000000001"},
  "victim":{"pid":0,"comm":"","memory_cgroup_css_addr":""},
  "memory_snapshot":{"host_meminfo":{"MemTotal":4096}}
 }}`
	task := triggerResult{Comm: "oom-mock", TriggerPID: 42, TriggerCSS: 0xffff000000000001}
	var valid document
	if err := json.Unmarshal([]byte(body), &valid); err != nil {
		t.Fatal(err)
	}
	if err := verifyScenario([]document{valid}, "null-victim", task, make(map[int]bool), nil); err != nil {
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
					var doc document
					if err := json.Unmarshal(encoded, &doc); err != nil {
						t.Fatal(err)
					}
					if err := verifyScenario([]document{doc}, "null-victim", task, make(map[int]bool), nil); err == nil {
						t.Fatal("missing or null actor data accepted")
					}
				})
			}
		}
	}
}

func TestNullControlFixtureReply(t *testing.T) {
	// The fixture keeps chosen=current for its reply, but passes NULL to the hook.
	task := triggerResult{
		Comm: "oom-mock", TriggerPID: 42, VictimPID: 42,
		TriggerCSS: 0xffff000000000001, VictimCSS: 0xffff000000000001,
	}
	got, err := expected("null-control", task)
	if err != nil || got != nil {
		t.Fatalf("null control: event=%+v, error=%v", got, err)
	}
}
