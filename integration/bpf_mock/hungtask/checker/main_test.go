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
	"fmt"
	"testing"
)

func TestVerifyPublishedEvents(t *testing.T) {
	const metadata = `"hostname":"node","region":"test","tracer_name":"hungtask","tracer_type":"event","tracer_id":"id","observed_timestamp":"2026-10-13T00:00:00Z","uploaded_timestamp":"2026-10-13T00:00:01Z"`
	var mainDoc, threadDoc document
	if err := json.Unmarshal([]byte(`{`+metadata+`,"tracer_data":{"tid":42,"comm":"mock","hung_task_timeout_secs":120,"cpus_stack":"","blocked_processes_stack":""}}`), &mainDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{`+metadata+`,"tracer_data":{"tid":44,"comm":"mock-thread-123","hung_task_timeout_secs":120,"cpus_stack":"","blocked_processes_stack":""}}`), &threadDoc); err != nil {
		t.Fatal(err)
	}
	tasks := map[string]triggerResult{"main": {PID: 42, TID: 42, Comm: "mock"}, "thread": {PID: 43, TID: 44, Comm: "mock-thread-123"}}
	if err := verifyFixtures([]document{mainDoc, threadDoc}, tasks, 120); err != nil {
		t.Fatal(err)
	}
	for _, documents := range [][]document{nil, {mainDoc}, {mainDoc, mainDoc}, {mainDoc, threadDoc, threadDoc}} {
		if err := verifyFixtures(documents, tasks, 120); err == nil {
			t.Fatal("missing or duplicate ES documents accepted")
		}
	}
	for _, change := range []func(*document){
		func(d *document) { d.Data.Comm = "wrong" },
		func(d *document) { d.Data.Stack = nil },
		func(d *document) { d.TracerType = "metric" },
		func(d *document) { d.Data.TID = 43 },
		func(d *document) { d.Data.Timeout = nil },
		func(d *document) { timeout := 119; d.Data.Timeout = &timeout },
		func(d *document) { d.Data.Blocked = nil },
	} {
		bad := threadDoc
		change(&bad)
		if err := verifyFixtures([]document{mainDoc, bad}, tasks, 120); err == nil {
			t.Fatal("invalid ES event accepted")
		}
	}
	tasks["thread"] = tasks["main"]
	if err := verifyFixtures([]document{mainDoc, threadDoc}, tasks, 120); err == nil {
		t.Fatal("worker fixture used main thread")
	}
}

func verifyFixtures(documents []document, tasks map[string]triggerResult, timeout int) error {
	matched := make(map[int]bool)
	for _, name := range []string{"main", "thread"} {
		if err := verifyScenario(documents, name, tasks[name], matched, timeout); err != nil {
			return err
		}
	}
	if len(matched) != len(documents) {
		return fmt.Errorf("unmatched ES documents")
	}
	return nil
}
