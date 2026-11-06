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

package pod

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestContainerTypeJSONRoundTrip(t *testing.T) {
	types := []ContainerType{
		ContainerTypeSidecar,
		ContainerTypeDaemonSet,
		ContainerTypeNode,
		ContainerTypeStatic,
		ContainerTypeNormal,
		ContainerTypeUnknown,
	}

	for _, want := range types {
		t.Run(want.String(), func(t *testing.T) {
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			var got ContainerType
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", data, err)
			}
			if got != want {
				t.Errorf("round trip = %v, want %v", got, want)
			}
		})
	}
}

func TestContainerQosJSONRoundTrip(t *testing.T) {
	levels := []ContainerQos{
		containerQosUnknown,
		containerQosGuaranteed,
		containerQosBurstable,
		containerQosBestEffort,
	}

	for _, want := range levels {
		t.Run(want.String(), func(t *testing.T) {
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			var got ContainerQos
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", data, err)
			}
			if got != want {
				t.Errorf("round trip = %v, want %v", got, want)
			}
		})
	}
}

func TestContainerQosUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want ContainerQos
	}{
		{name: "lowercase", data: `"guaranteed"`, want: containerQosGuaranteed},
		{name: "Kubernetes", data: `"Guaranteed"`, want: containerQosGuaranteed},
		{name: "uppercase", data: `"GUARANTEED"`, want: containerQosUnknown},
		{name: "invalid", data: `"invalid"`, want: containerQosUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ContainerQos
			if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tt.data, err)
			}
			if got != tt.want {
				t.Errorf("Unmarshal(%s) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestContainerQosUnmarshalJSONRejectsNonString(t *testing.T) {
	var qos ContainerQos
	if err := json.Unmarshal([]byte(`1`), &qos); err == nil {
		t.Fatal("Unmarshal(non-string) error = nil")
	}
}

func TestContainerQosStringInvalid(t *testing.T) {
	for _, qos := range []ContainerQos{-1, containerQosMax} {
		if got := qos.String(); got != "unknown" {
			t.Errorf("ContainerQos(%d).String() = %q, want unknown", qos, got)
		}
	}
}

func TestParseContainerQos(t *testing.T) {
	pod := &corev1.Pod{}
	pod.Status.QOSClass = corev1.PodQOSBestEffort

	got, err := parseContainerQos(ContainerTypeUnknown, pod)
	if err != nil {
		t.Fatalf("parseContainerQos() error = %v", err)
	}
	if got != containerQosBestEffort {
		t.Errorf("parseContainerQos() = %v, want %v", got, containerQosBestEffort)
	}
}

func TestContainerJSONUnknownValues(t *testing.T) {
	var typ ContainerType
	if err := json.Unmarshal([]byte(`"future"`), &typ); err != nil {
		t.Fatalf("Unmarshal(container type) error = %v", err)
	}
	if typ != ContainerTypeUnknown {
		t.Errorf("container type = %v, want unknown", typ)
	}

	var qos ContainerQos
	if err := json.Unmarshal([]byte(`"future"`), &qos); err != nil {
		t.Fatalf("Unmarshal(container qos) error = %v", err)
	}
	if qos != containerQosUnknown {
		t.Errorf("container qos = %v, want unknown", qos)
	}
}

// TestWorkloadCompatibilityPaths 验证不完整位置不会经旧容器视图回退到宿主机
// 根 cgroup；仅公共 Pod 可表达的独立网络目标也不能误投影为进程的网络。
func TestWorkloadCompatibilityPaths(t *testing.T) {
	for _, test := range []struct {
		name  string
		paths CgroupBinding
		want  bool
	}{
		{name: "missing paths"},
		{name: "host root", paths: CgroupBinding{Unified: "/"}},
		{name: "cpu only", paths: CgroupBinding{Controllers: map[string]string{"cpu": "/checkout"}}},
		{name: "memory masked", paths: CgroupBinding{Unified: "/checkout", Controllers: map[string]string{"memory": ""}}},
		{name: "invalid relative path", paths: CgroupBinding{Unified: "checkout"}},
		{name: "unclean path", paths: CgroupBinding{Unified: "/checkout/../host"}},
		{name: "unified", paths: CgroupBinding{Unified: "/checkout"}, want: true},
		{name: "split controllers", paths: CgroupBinding{Controllers: map[string]string{"cpu": "/checkout-cpu", "memory": "/checkout-memory"}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := &Pod{
				ID: "checkout", Type: TypeContainer, Cgroups: test.paths, Process: &HostProcess{PID: 42},
				Container: &ContainerInfo{Type: ContainerTypeNormal}, Labels: map[string]any{labelHostNamespace: "default"},
			}
			bound := raw.copyMetadata()
			bound.Network = &HostNetwork{PID: 43, NamespaceInum: 8500, NamespaceCookie: 8600, IPAddress: "172.16.0.2"}
			legacy := containerFromWorkload(raw, bound)
			if (legacy != nil) != test.want {
				t.Errorf("legacy view=%+v, want available=%t", legacy, test.want)
			}
			if legacy != nil && (legacy.NetNamespaceInum != 0 || legacy.NetNamespaceCookie != 0) {
				t.Error("independent network target was assigned to legacy process PID")
			}
		})
	}
}

// TestWorkloadCompatibilityMetadata 验证缺失容器分类或旧 namespace label 时
// 不创建会使旧文档/指标构造器 panic 的兼容视图。
func TestWorkloadCompatibilityMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		info   *ContainerInfo
		labels map[string]any
	}{
		{name: "missing container metadata", labels: map[string]any{labelHostNamespace: "default"}},
		{name: "missing namespace label", info: &ContainerInfo{Type: ContainerTypeNormal}},
		{name: "missing Kata sandbox identity", info: &ContainerInfo{Type: ContainerTypeNormal, RuntimeType: "io.containerd.kata-qemu.v2"}, labels: map[string]any{labelHostNamespace: "default"}},
		{name: "wrong namespace type", info: &ContainerInfo{Type: ContainerTypeNormal}, labels: map[string]any{labelHostNamespace: 42}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := &Pod{ID: "checkout", Type: TypeContainer, Cgroups: CgroupBinding{Unified: "/checkout"}, Container: test.info, Labels: test.labels}
			if legacy := containerFromWorkload(raw, raw); legacy != nil {
				t.Errorf("unrepresentable legacy metadata=%+v", legacy)
			}
		})
	}
}

// TestWorkloadKataCompatibilityCSS 验证公共绑定有多个控制器时，旧 Kata
// 视图仍只提供 CPU 采样地址，避免把 VM 内存事件归到每个应用容器。
func TestWorkloadKataCompatibilityCSS(t *testing.T) {
	workload := &Pod{
		ID: "checkout", Type: TypeContainer,
		Cgroups:   CgroupBinding{Unified: "/checkout", CSS: map[string]uint64{"cpu": 8500, "memory": 8600}},
		Container: &ContainerInfo{Type: ContainerTypeNormal, RuntimeType: "io.containerd.kata-qemu.v2", SandboxID: "checkout-vm"},
		Labels:    map[string]any{labelHostNamespace: "default"},
	}
	legacy := containerFromWorkload(workload, workload)
	if legacy == nil || len(legacy.CgroupCss) != 1 || legacy.CgroupCss["cpu"] != 8500 {
		t.Errorf("Kata CSS projection=%+v", legacy)
	}
	if len(workload.Cgroups.CSS) != 2 {
		t.Error("Kata compatibility mutated the public binding")
	}
}
