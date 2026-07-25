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

package dashboards

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	cpuProfileType    = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	memoryProfileType = "memory:alloc_space:bytes:space:bytes"
)

type dashboardVariable struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Query      string `json:"query"`
	Options    []struct {
		Value string `json:"value"`
	} `json:"options"`
}

type dashboardContract struct {
	UID        string `json:"uid"`
	Templating struct {
		List []dashboardVariable `json:"list"`
	} `json:"templating"`
}

func loadDashboard(t *testing.T, filename string) (dashboardContract, any, string) {
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

	return contract, raw, string(payload)
}

func dashboardStrings(value any, field string, values *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for name, item := range typed {
			if name == field {
				if text, ok := item.(string); ok {
					*values = append(*values, text)
				}
			}
			dashboardStrings(item, field, values)
		}
	case []any:
		for _, item := range typed {
			dashboardStrings(item, field, values)
		}
	}
}

func findDashboardVariable(t *testing.T, dashboard dashboardContract, name string) dashboardVariable {
	t.Helper()

	for _, variable := range dashboard.Templating.List {
		if variable.Name == name {
			return variable
		}
	}
	t.Fatalf("dashboard %q has no %q variable", dashboard.UID, name)
	return dashboardVariable{}
}

func requireSupportedProfileTypes(t *testing.T, dashboard dashboardContract) {
	t.Helper()

	variable := findDashboardVariable(t, dashboard, "type")
	values := make([]string, 0, len(variable.Options))
	for _, option := range variable.Options {
		values = append(values, option.Value)
	}
	if !slices.Equal(values, []string{cpuProfileType, memoryProfileType}) {
		t.Fatalf("profile types = %v, want only CPU and memory", values)
	}
}

func TestContinuousProfilingHostDashboardContract(t *testing.T) {
	dashboard, raw, payload := loadDashboard(t, "continuous-profiling-host.json")
	if dashboard.UID != "continuous-profiling-host" {
		t.Fatalf("UID = %q, want continuous-profiling-host", dashboard.UID)
	}
	requireSupportedProfileTypes(t, dashboard)

	hostname := findDashboardVariable(t, dashboard, "hostname")
	if !strings.Contains(hostname.Definition, "NOT _exists_:container_hostname") {
		t.Fatalf("hostname query can select container profiles: %s", hostname.Definition)
	}

	var selectors []string
	dashboardStrings(raw, "labelSelector", &selectors)
	if !slices.Equal(selectors, []string{`{hostname="$hostname"}`}) {
		t.Fatalf("profile selectors = %v, want exact hostname selector", selectors)
	}
	if strings.Contains(payload, "process_lock") {
		t.Fatal("dashboard exposes lock profiling before the backend supports it")
	}
}

func TestContinuousProfilingContainerDashboardContract(t *testing.T) {
	dashboard, raw, payload := loadDashboard(t, "continuous-profiling-container.json")
	if dashboard.UID != "continuous-profiling-container" {
		t.Fatalf("UID = %q, want continuous-profiling-container", dashboard.UID)
	}
	requireSupportedProfileTypes(t, dashboard)

	containerID := findDashboardVariable(t, dashboard, "container_id")
	if !strings.Contains(containerID.Definition, `"field": "container_id.keyword"`) {
		t.Fatalf("container variable does not use container ID: %s", containerID.Definition)
	}
	hostname := findDashboardVariable(t, dashboard, "hostname")
	if !strings.Contains(hostname.Definition, "container_id.keyword:$container_id") {
		t.Fatalf("hostname variable is not container scoped: %s", hostname.Definition)
	}

	var selectors []string
	dashboardStrings(raw, "labelSelector", &selectors)
	wantSelector := `{hostname="$hostname",container_id="$container_id"}`
	if !slices.Equal(selectors, []string{wantSelector}) {
		t.Fatalf("profile selectors = %v, want %s", selectors, wantSelector)
	}
	if strings.Contains(payload, "container_hostname") {
		t.Fatal("dashboard uses non-unique container hostname")
	}
	if strings.Contains(payload, "process_lock") {
		t.Fatal("dashboard exposes lock profiling before the backend supports it")
	}
}

func TestContinuousProfilingDatasourceAuthenticationContract(t *testing.T) {
	datasource, err := os.ReadFile("../datasources/pyroscope.yaml")
	if err != nil {
		t.Fatalf("read Pyroscope datasource: %v", err)
	}
	datasourceText := string(datasource)
	if !strings.Contains(
		datasourceText,
		"'Bearer $HUATUO_GRAFANA_PROFILE_TOKEN'",
	) {
		t.Fatal("Pyroscope datasource does not use the runtime bearer token")
	}
	if strings.Contains(datasourceText, "REPLACE_WITH_RANDOM_HEX") {
		t.Fatal("Pyroscope datasource contains a placeholder credential")
	}

	compose, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatalf("read Docker Compose config: %v", err)
	}
	if !strings.Contains(
		string(compose),
		"HUATUO_GRAFANA_PROFILE_TOKEN: ${HUATUO_GRAFANA_PROFILE_TOKEN:-}",
	) {
		t.Fatal("Grafana container does not receive the profile query token")
	}
}
