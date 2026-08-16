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
	AllValue   string `json:"allValue"`
	IncludeAll bool   `json:"includeAll"`
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

func dashboardValues(value any, field string, values *[]any) {
	switch typed := value.(type) {
	case map[string]any:
		for name, item := range typed {
			if name == field {
				*values = append(*values, item)
			}
			dashboardValues(item, field, values)
		}
	case []any:
		for _, item := range typed {
			dashboardValues(item, field, values)
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

func requireExactProfileQueries(
	t *testing.T,
	raw any,
	selector string,
) {
	t.Helper()

	var selectors []string
	dashboardStrings(raw, "labelSelector", &selectors)
	if !slices.Equal(selectors, []string{selector, selector, selector}) {
		t.Fatalf("profile selectors = %v, want exact %s selectors", selectors, selector)
	}

	var queryTypes []string
	dashboardStrings(raw, "queryType", &queryTypes)
	if !slices.Equal(queryTypes, []string{"metrics", "metrics", "profile"}) {
		t.Fatalf("query types = %v, want two series and one profile query", queryTypes)
	}

	var groupBys []any
	dashboardValues(raw, "groupBy", &groupBys)
	if len(groupBys) != 3 {
		t.Fatalf("profile groupBy values = %v, want three", groupBys)
	}
	for _, value := range groupBys {
		grouped, ok := value.([]any)
		if !ok {
			t.Fatalf("profile groupBy value = %v, want an array", value)
		}
		for _, label := range grouped {
			name, ok := label.(string)
			if !ok {
				t.Fatalf("profile groupBy label = %v, want a string", label)
			}
			if strings.Contains(name, "$") {
				t.Fatalf("profile groupBy label %q contains an unsupported variable", name)
			}
		}
	}
	if grouped, ok := groupBys[1].([]any); !ok ||
		len(grouped) != 1 ||
		grouped[0] != "tracer" {
		t.Fatalf("Top series groupBy = %v, want tracer", groupBys[1])
	}

	var limits []any
	dashboardValues(raw, "limit", &limits)
	profileLimits := make([]float64, 0, len(limits))
	for _, limit := range limits {
		if number, ok := limit.(float64); ok {
			profileLimits = append(profileLimits, number)
		}
	}
	if !slices.Contains(profileLimits, float64(10)) {
		t.Fatalf("dashboard limits = %v, want Top 10 bound", profileLimits)
	}
}

func requireDimensionVariables(
	t *testing.T,
	dashboard dashboardContract,
) {
	t.Helper()

	fields := map[string]string{
		"profiling_scope": "tracer_data.flamedata.labels.profiling_scope.keyword",
		"cpu":             "tracer_data.flamedata.labels.cpu.keyword",
		"pid":             "tracer_data.flamedata.labels.pid.keyword",
		"tgid":            "tracer_data.flamedata.labels.tgid.keyword",
	}
	for name, field := range fields {
		variable := findDashboardVariable(t, dashboard, name)
		if !variable.IncludeAll || variable.AllValue != "__all" {
			t.Fatalf("%s must provide the explicit All sentinel", name)
		}
		if !strings.Contains(variable.Definition, field) ||
			!strings.Contains(variable.Definition, `"size": 500`) {
			t.Fatalf("%s query is unbounded or uses the wrong field: %s", name, variable.Definition)
		}
	}
}

func requireNoUnsupportedSelectors(t *testing.T, payload string) {
	t.Helper()

	for _, unsupported := range []string{
		"cgroup",
		"process_group",
		"process_lock",
		"lock_type",
		`=~`,
		`!~`,
	} {
		if strings.Contains(payload, unsupported) {
			t.Fatalf("dashboard contains unsupported selector %q", unsupported)
		}
	}
}

func TestContinuousProfilingHostDashboardContract(t *testing.T) {
	dashboard, raw, payload := loadDashboard(t, "continuous-profiling-host.json")
	if dashboard.UID != "continuous-profiling-host" {
		t.Fatalf("UID = %q, want continuous-profiling-host", dashboard.UID)
	}
	requireSupportedProfileTypes(t, dashboard)
	requireDimensionVariables(t, dashboard)

	hostname := findDashboardVariable(t, dashboard, "hostname")
	if !strings.Contains(hostname.Definition, "NOT _exists_:container_hostname") {
		t.Fatalf("hostname query can select container profiles: %s", hostname.Definition)
	}

	requireExactProfileQueries(
		t,
		raw,
		`{hostname="$hostname",profiling_scope="$profiling_scope",`+
			`cpu="$cpu",pid="$pid",tgid="$tgid"}`,
	)
	requireNoUnsupportedSelectors(t, payload)
}

func TestContinuousProfilingContainerDashboardContract(t *testing.T) {
	dashboard, raw, payload := loadDashboard(t, "continuous-profiling-container.json")
	if dashboard.UID != "continuous-profiling-container" {
		t.Fatalf("UID = %q, want continuous-profiling-container", dashboard.UID)
	}
	requireSupportedProfileTypes(t, dashboard)
	requireDimensionVariables(t, dashboard)

	containerID := findDashboardVariable(t, dashboard, "container_id")
	if !strings.Contains(containerID.Definition, `"field": "container_id.keyword"`) {
		t.Fatalf("container variable does not use container ID: %s", containerID.Definition)
	}
	hostname := findDashboardVariable(t, dashboard, "hostname")
	if !strings.Contains(hostname.Definition, "container_id.keyword:$container_id") {
		t.Fatalf("hostname variable is not container scoped: %s", hostname.Definition)
	}

	requireExactProfileQueries(
		t,
		raw,
		`{container_id="$container_id",profiling_scope="$profiling_scope",`+
			`cpu="$cpu",pid="$pid",tgid="$tgid"}`,
	)
	if strings.Contains(payload, "container_hostname") {
		t.Fatal("dashboard uses non-unique container hostname")
	}
	requireNoUnsupportedSelectors(t, payload)
}

func TestContinuousProfilingComparisonDashboardContract(t *testing.T) {
	dashboard, raw, payload := loadDashboard(
		t,
		"continuous-profiling-compare.json",
	)
	if dashboard.UID != "continuous-profiling-compare" {
		t.Fatalf("UID = %q, want continuous-profiling-compare", dashboard.UID)
	}
	requireSupportedProfileTypes(t, dashboard)
	requireDimensionVariables(t, dashboard)

	containerID := findDashboardVariable(t, dashboard, "container_id")
	if !strings.Contains(containerID.Definition, `"field": "container_id.keyword"`) {
		t.Fatalf("container variable does not use container ID: %s", containerID.Definition)
	}
	if !containerID.IncludeAll || containerID.AllValue != "__all" {
		t.Fatal("container_id must provide the explicit All sentinel")
	}

	var urls []string
	dashboardStrings(raw, "url", &urls)
	var diffURL string
	for _, url := range urls {
		if strings.HasPrefix(url, "/v1/profiles/flamegraph/diff-rows?") {
			diffURL = url
			break
		}
	}
	if diffURL == "" {
		t.Fatalf("comparison URLs = %v, want bounded diff adapter", urls)
	}
	for _, parameter := range []string{
		"profile_type_id=${type:percentencode}",
		"hostname=${hostname:percentencode}",
		"container_id=${container_id:percentencode}",
		"profiling_scope=${profiling_scope:percentencode}",
		"cpu=${cpu:percentencode}",
		"pid=${pid:percentencode}",
		"tgid=${tgid:percentencode}",
		"start=${__from}",
		"end=${__to}",
		"max_nodes=5000",
	} {
		if !strings.Contains(diffURL, parameter) {
			t.Fatalf("comparison URL %q is missing %q", diffURL, parameter)
		}
	}
	var methods []string
	dashboardStrings(raw, "method", &methods)
	if !slices.Equal(methods, []string{"POST"}) {
		t.Fatalf("comparison methods = %v, want bodyless POST", methods)
	}
	var bodies []string
	dashboardStrings(raw, "data", &bodies)
	if len(bodies) != 0 {
		t.Fatalf("comparison request must not interpolate raw JSON: %v", bodies)
	}
	var roots []string
	dashboardStrings(raw, "root_selector", &roots)
	if !slices.Equal(roots, []string{"data"}) {
		t.Fatalf("root selectors = %v, want data", roots)
	}
	var columns []string
	dashboardStrings(raw, "selector", &columns)
	if !slices.Equal(
		columns,
		[]string{"level", "value", "self", "valueRight", "selfRight", "label"},
	) {
		t.Fatalf("comparison columns = %v", columns)
	}
	requireNoUnsupportedSelectors(t, payload)
}

func TestContinuousProfilingDatasourceAuthenticationContract(t *testing.T) {
	for _, filename := range []string{
		"../datasources/pyroscope.yaml",
		"../datasources/profiling-json.yaml",
	} {
		datasource, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		datasourceText := string(datasource)
		if !strings.Contains(
			datasourceText,
			"'Bearer $HUATUO_GRAFANA_PROFILE_TOKEN'",
		) {
			t.Fatalf("%s does not use the runtime bearer token", filename)
		}
		if strings.Contains(datasourceText, "REPLACE_WITH_RANDOM_HEX") {
			t.Fatalf("%s contains a placeholder credential", filename)
		}
	}
	pyroscope, err := os.ReadFile("../datasources/pyroscope.yaml")
	if err != nil {
		t.Fatalf("read Pyroscope datasources: %v", err)
	}
	pyroscopeText := string(pyroscope)
	for _, expected := range []string{
		"uid: huatuo-apiserver-pyroscope",
		"url: http://127.0.0.1:12740/v1/profiles/flamegraph/",
	} {
		if !strings.Contains(pyroscopeText, expected) {
			t.Fatalf("Pyroscope datasources missing %q", expected)
		}
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
	if !strings.Contains(
		string(compose),
		"./grafana/datasources/profiling-json.yaml:",
	) {
		t.Fatal("Grafana does not provision the profile JSON datasource")
	}
}
