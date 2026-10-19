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

func TestVerifyReclaimEvents(t *testing.T) {
	const body = `{"hostname":"node","region":"dev","tracer_name":"memory_reclaim",
"tracer_type":"event","tracer_id":"id","container_id":"container-id",
"observed_timestamp":"2026-10-19T00:00:00Z","uploaded_timestamp":"2026-10-19T00:00:01Z",
"tracer_data":{"pid":42,"tid":42,"comm":"reclaim-mock","reclaim_duration_ns":50000000}}`
	expectedRaw := triggerOutput{PID: 42, TID: 42, TriggerComm: "reclaim-mock", ElapsedNS: 100000000}
	for _, tc := range []struct {
		name   string
		change func(*document)
	}{
		{name: "valid"},
		{name: "wrong PID", change: func(d *document) { d.Data.PID++ }},
		{name: "wrong TID", change: func(d *document) { d.Data.TID++ }},
		{name: "wrong command", change: func(d *document) { d.Data.Comm = "wrong" }},
		{name: "wrong container", change: func(d *document) { d.ContainerID = "wrong" }},
		{name: "missing container", change: func(d *document) { d.ContainerID = "" }},
		{name: "wrong tracer", change: func(d *document) { d.TracerName = "memory_reclaim_events" }},
		{name: "missing metadata", change: func(d *document) { d.Observed = "" }},
		{name: "zero duration", change: func(d *document) { d.Data.DurationNS = 0 }},
		{name: "short duration", change: func(d *document) { d.Data.DurationNS = 19999999 }},
		{name: "duration exceeds ioctl", change: func(d *document) { d.Data.DurationNS = 100000001 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actualDocument document
			if err := json.Unmarshal([]byte(body), &actualDocument); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&actualDocument)
			}
			err := verifyScenario([]document{actualDocument}, "report-main", expectedRaw, "container-id", 1000000000)
			if (err != nil) != (tc.change != nil) {
				t.Fatalf("unexpected verification result: %v", err)
			}
		})
	}
	var actualDocument document
	if err := json.Unmarshal([]byte(body), &actualDocument); err != nil {
		t.Fatal(err)
	}
	t.Run("reused PID and TID", func(t *testing.T) {
		secondDocument := actualDocument
		secondIdentity := expectedRaw
		secondIdentity.TriggerComm = "second-trigger"
		secondDocument.Data.Comm = secondIdentity.TriggerComm
		documents := []document{actualDocument, secondDocument}
		for _, identity := range []triggerOutput{expectedRaw, secondIdentity} {
			if err := verifyScenario(documents, "report-main", identity, "container-id", 1000000000); err != nil {
				t.Fatal(err)
			}
		}
		secondIdentity.TriggerComm = "filtered"
		if err := verifyScenario(documents, "filtered-main", secondIdentity, "container-id", 1000000000); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("document count", func(t *testing.T) {
		dir := t.TempDir()
		identityJSON, err := json.Marshal(expectedRaw)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string][]byte{
			"events.json":      []byte(body),
			"report-main.json": identityJSON,
			"container.json":   []byte(`{"data":{"id":"container-id","cgroup_css":{"cpu":"0x1000"}}}`),
		} {
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		previousArgs := os.Args
		t.Cleanup(func() { os.Args = previousArgs })
		os.Args = []string{"reclaim-checker", filepath.Join(dir, "events.json"), dir, filepath.Join(dir, "container.json"), "1000000000", "report-main"}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Args[1], []byte(body+"\n"+`{"tracer_data":{"comm":"unrelated"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(); err == nil {
			t.Fatal("unexpected ES document accepted")
		}
	})
	for _, actualDocuments := range [][]document{nil, {actualDocument, actualDocument}} {
		if err := verifyScenario(actualDocuments, "report-main", expectedRaw, "container-id", 1000000000); err == nil {
			t.Fatal("missing or duplicate events accepted")
		}
	}
	for _, scenario := range []string{"host-main", "filtered-main"} {
		if err := verifyScenario(nil, scenario, expectedRaw, "container-id", 1000000000); err != nil {
			t.Fatal(err)
		}
		if err := verifyScenario([]document{actualDocument}, scenario, expectedRaw, "container-id", 1000000000); err == nil {
			t.Fatalf("%s unexpectedly produced an event", scenario)
		}
	}
	if err := verifyScenario(nil, "filtered-main", expectedRaw, "container-id", expectedRaw.ElapsedNS); err == nil {
		t.Fatal("filter test exceeded its threshold")
	}
	expectedRaw.TID = 43
	expectedRaw.TriggerComm = "reclaim-worker-"
	actualDocument.Data.TID = expectedRaw.TID
	actualDocument.Data.Comm = expectedRaw.TriggerComm
	if err := verifyScenario([]document{actualDocument}, "report-thread", expectedRaw, "container-id", 1000000000); err != nil {
		t.Fatal(err)
	}
	if err := verifyScenario(nil, "filtered-thread", expectedRaw, "container-id", 1000000000); err != nil {
		t.Fatal(err)
	}
	expectedRaw.TID = expectedRaw.PID
	if err := verifyScenario(nil, "filtered-thread", expectedRaw, "container-id", 1000000000); err == nil {
		t.Fatal("worker used main thread identity")
	}
}
