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
	"os/exec"
	"path"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func TestMountedConfigIsPassedToCollector(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is required to render chart regression tests")
	}
	for _, mountPath := range []string{
		"/home/huatuo-bamai/conf/huatuo-bamai.conf",
		"/opt/huatuo settings/custom agent.conf",
	} {
		t.Run(mountPath, func(t *testing.T) {
			cmd := exec.Command(helm, "template", "config-test", ".",
				"--show-only", "templates/daemonset.yaml",
				"--set-string", "config.content=[Log]",
				"--set-string", "config.fileName=config-key.conf",
				"--set-string", "config.mountPath="+mountPath)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, output)
			}
			var daemonSet struct {
				Spec struct {
					Template corev1.PodTemplateSpec `json:"template"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(output, &daemonSet); err != nil {
				t.Fatal(err)
			}
			containers := daemonSet.Spec.Template.Spec.Containers
			if len(containers) != 1 {
				t.Fatalf("want one collector container, got %d", len(containers))
			}
			container := containers[0]
			if !reflect.DeepEqual(container.Command, []string{"./bin/huatuo-bamai"}) {
				t.Errorf("collector command = %v", container.Command)
			}
			wantArgs := []string{
				"--region", "example", "--config-dir", path.Dir(mountPath),
				"--config", path.Base(mountPath),
			}
			if !reflect.DeepEqual(container.Args, wantArgs) {
				t.Errorf("collector args = %v, want %v", container.Args, wantArgs)
			}
			for _, mount := range container.VolumeMounts {
				if mount.Name == "huatuo-bamai-config-volume" {
					if mount.MountPath != mountPath || mount.SubPath != "config-key.conf" {
						t.Fatalf("config mount = %+v", mount)
					}
					return
				}
			}
			t.Fatal("configuration volume mount is missing")
		})
	}
}
