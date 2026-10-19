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
	var actualMainDocument, actualThreadDocument document
	if err := json.Unmarshal([]byte(`{`+metadata+`,"tracer_data":{"tid":42,"comm":"mock","hung_task_timeout_secs":120,"cpus_stack":"","blocked_processes_stack":""}}`), &actualMainDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{`+metadata+`,"tracer_data":{"tid":44,"comm":"mock-thread-123","hung_task_timeout_secs":120,"cpus_stack":"","blocked_processes_stack":""}}`), &actualThreadDocument); err != nil {
		t.Fatal(err)
	}
	triggerOutputs := map[string]triggerOutput{"main": {PID: 42, TID: 42, TriggerComm: "mock"}, "thread": {PID: 43, TID: 44, TriggerComm: "mock-thread-123"}}
	if err := verifyScenarios([]document{actualMainDocument, actualThreadDocument}, triggerOutputs, 120); err != nil {
		t.Fatal(err)
	}
	t.Run("reused TID", func(t *testing.T) {
		reusedDocument := actualThreadDocument
		reusedDocument.Data.TID = actualMainDocument.Data.TID
		identities := map[string]triggerOutput{
			"main":   triggerOutputs["main"],
			"thread": {PID: 41, TID: 42, TriggerComm: "mock-thread-123"},
		}
		if err := verifyScenarios([]document{actualMainDocument, reusedDocument}, identities, 120); err != nil {
			t.Fatal(err)
		}
	})
	unrelated := actualThreadDocument
	unrelated.Data.Comm = "unrelated"
	if err := verifyScenarios([]document{actualMainDocument, actualThreadDocument, unrelated}, triggerOutputs, 120); err == nil {
		t.Fatal("unexpected ES document accepted")
	}
	for _, actualDocuments := range [][]document{nil, {actualMainDocument}, {actualMainDocument, actualMainDocument}, {actualMainDocument, actualThreadDocument, actualThreadDocument}} {
		if err := verifyScenarios(actualDocuments, triggerOutputs, 120); err == nil {
			t.Fatal("missing or duplicate ES documents accepted")
		}
	}
	for _, change := range []func(*document){
		func(actualDocument *document) { actualDocument.Data.Comm = "wrong" },
		func(actualDocument *document) { actualDocument.Data.Stack = nil },
		func(actualDocument *document) { actualDocument.TracerType = "metric" },
		func(actualDocument *document) { actualDocument.Data.TID = 43 },
		func(actualDocument *document) { actualDocument.Data.Timeout = nil },
		func(actualDocument *document) { actualTimeout := 119; actualDocument.Data.Timeout = &actualTimeout },
		func(actualDocument *document) { actualDocument.Data.Blocked = nil },
	} {
		invalidDocument := actualThreadDocument
		change(&invalidDocument)
		if err := verifyScenarios([]document{actualMainDocument, invalidDocument}, triggerOutputs, 120); err == nil {
			t.Fatal("invalid ES event accepted")
		}
	}
	triggerOutputs["thread"] = triggerOutputs["main"]
	if err := verifyScenarios([]document{actualMainDocument, actualThreadDocument}, triggerOutputs, 120); err == nil {
		t.Fatal("worker fixture used main thread")
	}
}

func verifyScenarios(actualDocuments []document, triggerOutputs map[string]triggerOutput, expectedTimeout int) error {
	for _, scenario := range []string{"main", "thread"} {
		if err := verifyScenario(actualDocuments, scenario, triggerOutputs[scenario], expectedTimeout); err != nil {
			return err
		}
	}
	if len(actualDocuments) != 2 {
		return fmt.Errorf("ES document count: got %d, want 2", len(actualDocuments))
	}
	return nil
}
