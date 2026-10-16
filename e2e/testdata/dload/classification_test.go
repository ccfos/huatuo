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

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/matcher"

	"github.com/pelletier/go-toml"
)

func TestIssueRules(t *testing.T) {
	rules := issueRules()
	if err := matcher.ValidateClassifications(rules); err != nil {
		t.Fatal(err)
	}
	const header = "\nstacktrace of D task in cgroup:\nComm: dload-e2e\tPid: 123\n"
	const frames = "[<0>] arbitrary_wait+0x10/0x20\n[<0>] arbitrary_caller+0x10/0x20\n"
	for _, tc := range []struct {
		name  string
		stack string
		want  string
	}{
		{"arbitrary kernel frames", header + frames, "e2e-dload"},
		{"header only", header, "e2e-dload"},
		{"different comm", strings.ReplaceAll(header, "dload-e2e", "other-worker") + frames, "none"},
		{"comm suffix", strings.ReplaceAll(header, "dload-e2e", "dload-e2e-other"), "none"},
		{"comm prefix", strings.ReplaceAll(header, "dload-e2e", "other-dload-e2e"), "none"},
		{"invalid PID", strings.ReplaceAll(header, "123", "invalid"), "none"},
		{"not a task header", "prefix Comm: dload-e2e\tPid: 123\n", "none"},
		{"first rule", strings.ReplaceAll(header, "dload-e2e", "dload-unmatched"), "e2e-unmatched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue, matched := matcher.Classify(rules, tc.stack)
			if matched != (tc.want != "none") || issue != tc.want {
				t.Fatalf("Classify() = %q, %t; want %q", issue, matched, tc.want)
			}
		})
	}
}

func TestIssueRulesConfigEncoding(t *testing.T) {
	rules := issueRules()
	data, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		IssuesList [][]string `toml:"IssuesList"`
	}
	if err := toml.NewDecoder(strings.NewReader("IssuesList = " + string(data))).Strict(true).Decode(&config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.IssuesList, rules) {
		t.Fatalf("TOML decoding changed the classification rules: %v", config.IssuesList)
	}
}
