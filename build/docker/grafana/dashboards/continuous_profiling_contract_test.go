// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dashboards

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type dashboardTemplateVariable struct {
	Name       string `json:"name"`
	AllValue   string `json:"allValue"`
	Definition string `json:"definition"`
	Query      string `json:"query"`
}

type dashboardContract struct {
	UID        string `json:"uid"`
	Templating struct {
		List []dashboardTemplateVariable `json:"list"`
	} `json:"templating"`
}

func loadDashboardContract(t *testing.T, filename string) (dashboardContract, any) {
	t.Helper()
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	var contract dashboardContract
	if err := json.Unmarshal(payload, &contract); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode raw %s: %v", filename, err)
	}
	return contract, raw
}

func collectStringFields(value any, field string, values *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for name, item := range typed {
			if name == field {
				if text, ok := item.(string); ok {
					*values = append(*values, text)
				}
			}
			collectStringFields(item, field, values)
		}
	case []any:
		for _, item := range typed {
			collectStringFields(item, field, values)
		}
	}
}

func templateVariable(t *testing.T, contract dashboardContract, name string) dashboardTemplateVariable {
	t.Helper()
	for _, variable := range contract.Templating.List {
		if variable.Name == name {
			return variable
		}
	}
	t.Fatalf("dashboard %s has no %q template variable", contract.UID, name)
	return dashboardTemplateVariable{}
}

func TestContinuousProfilingDashboardContracts(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		uid      string
	}{
		{name: "host", filename: "continuous-profiling-host.json", uid: "continuous-profiling-host"},
		{name: "container", filename: "continuous-profiling-container.json", uid: "continuous-profiling-container"},
		{name: "compare", filename: "continuous-profiling-compare.json", uid: "continuous-profiling-compare"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contract, raw := loadDashboardContract(t, tc.filename)
			if contract.UID != tc.uid {
				t.Fatalf("UID = %q, want %q", contract.UID, tc.uid)
			}

			var selectors []string
			collectStringFields(raw, "labelSelector", &selectors)
			if len(selectors) == 0 {
				t.Fatal("dashboard has no profile labelSelector")
			}
			for _, selector := range selectors {
				if !strings.Contains(selector, `hostname="$hostname"`) {
					t.Fatalf("selector does not pin hostname: %s", selector)
				}
				if tc.name == "container" && !strings.Contains(selector, `container_hostname="$container_hostname"`) {
					t.Fatalf("container selector does not pin container: %s", selector)
				}
				if tc.name == "compare" && !strings.Contains(selector, `container_hostname=~"${container_hostname:regex}"`) {
					t.Fatalf("comparison selector has no host/container switch: %s", selector)
				}
			}

			switch tc.name {
			case "host":
				for _, variable := range contract.Templating.List {
					if variable.Name == "hostname" || variable.Name == "type" || variable.Name == "group_by" {
						continue
					}
					if variable.Definition != "" && !strings.Contains(variable.Definition, "NOT _exists_:container_hostname") {
						t.Fatalf("host variable %q can include container values: %s", variable.Name, variable.Definition)
					}
				}
			case "container":
				for _, variable := range contract.Templating.List {
					if variable.Name == "hostname" {
						if variable.Definition != "" && !strings.Contains(variable.Definition, "container_hostname.keyword:$container_hostname") {
							t.Fatalf("container hostname variable is not container scoped: %s", variable.Definition)
						}
						continue
					}
					if variable.Name == "container_hostname" || variable.Name == "type" || variable.Name == "group_by" {
						continue
					}
					if variable.Definition != "" && (!strings.Contains(variable.Definition, "hostname.keyword:$hostname") ||
						!strings.Contains(variable.Definition, "container_hostname.keyword:$container_hostname")) {
						t.Fatalf("container variable %q is not host/container scoped: %s", variable.Name, variable.Definition)
					}
				}
			case "compare":
				if got := templateVariable(t, contract, "container_hostname").AllValue; got != "^$" {
					t.Fatalf("comparison container AllValue = %q, want ^$ for host profiles", got)
				}
			}
		})
	}
}
