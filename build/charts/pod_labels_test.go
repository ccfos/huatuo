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

package charts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func TestPodLabelsPreserveDaemonSetSelector(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is required to render the chart")
	}
	for _, tc := range []struct{ name, labels string }{
		{"default", "{}"},
		{"custom", "{team: observability}"},
		{"matching_selector", "{app: huatuo, team: observability}"},
		{"conflicting_selector", "{app: custom, app.kubernetes.io/name: other, app.kubernetes.io/instance: other-release, team: observability}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := filepath.Join(t.TempDir(), "values.yaml")
			require.NoError(t, os.WriteFile(values, []byte("podLabels: "+tc.labels+"\n"), 0o600))
			output, err := exec.CommandContext(t.Context(), helm, "template", "label-check", ".",
				"--values", values, "--show-only", "templates/daemonset.yaml").CombinedOutput()
			require.NoError(t, err, "%s", output)
			var daemonSet struct {
				Spec struct {
					Selector struct {
						MatchLabels map[string]string `json:"matchLabels"`
					} `json:"selector"`
					Template corev1.PodTemplateSpec `json:"template"`
				} `json:"spec"`
			}
			// Reject duplicate keys as well as selector mismatches.
			data, err := yaml.YAMLToJSONStrict(output)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &daemonSet))
			require.Len(t, daemonSet.Spec.Selector.MatchLabels, 3)
			for key, value := range daemonSet.Spec.Selector.MatchLabels {
				require.Equal(t, value, daemonSet.Spec.Template.Labels[key], "selector label %q", key)
			}
			if tc.name != "default" {
				require.Equal(t, "observability", daemonSet.Spec.Template.Labels["team"])
			}
		})
	}
}
