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

package profiling

import (
	"strings"
	"testing"
	"time"

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/job"
)

func TestGetFlameGraphURLEscapesLabelValue(t *testing.T) {
	cfg := config.Get()
	oldBase := cfg.Profiling.FlameGraphBaseURL
	cfg.Profiling.FlameGraphBaseURL = "http://grafana.example/d"
	defer func() { cfg.Profiling.FlameGraphBaseURL = oldBase }()

	url := getFlameGraphURL(&job.Job{
		Type:      ProfilingCPU,
		Container: "container+2026&debug",
		StartTime: time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
	})

	if !strings.Contains(url, "var-container_hostname=container%2B2026%26debug") {
		t.Fatalf("url = %q, want escaped container label value", url)
	}
}
