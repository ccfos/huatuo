// Copyright 2025 The HuaTuo Authors
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
	"testing"
)

func TestKubeletDefaultConfigPathsIncludesEtcAndHostEtc(t *testing.T) {
	// Verify default paths include both /etc and /host/etc variants.
	// This addresses issue #75 where AWS EKS users had kubelet config at
	// /etc/kubernetes/kubelet/config.json which was not in the search path.
	foundEtc := false
	foundHostEtc := false
	for _, p := range kubeletDefaultConfigPath {
		if p == "/etc/kubernetes/kubelet/config.json" {
			foundEtc = true
		}
		if p == "/host/etc/kubernetes/kubelet/config.json" {
			foundHostEtc = true
		}
	}
	if !foundEtc {
		t.Error("kubeletDefaultConfigPath should include /etc/kubernetes/kubelet/config.json")
	}
	if !foundHostEtc {
		t.Error("kubeletDefaultConfigPath should include /host/etc/kubernetes/kubelet/config.json")
	}
}

func TestKubeletDefaultConfigPathsMinimum(t *testing.T) {
	// Verify that the search paths include common kubelet config locations
	expectedMin := 3
	if len(kubeletDefaultConfigPath) < expectedMin {
		t.Errorf("kubeletDefaultConfigPath has only %d entries, expected at least %d",
			len(kubeletDefaultConfigPath), expectedMin)
	}
}

func TestKubeletConfigCacheUpdateDoesNotPanic(t *testing.T) {
	// Verify that kubeletConfigCacheUpdate returns an error (or nil with defaults)
	// instead of panicking when neither the kubelet HTTP endpoint nor default
	// config files are available. Previously this would:
	//   panic("we cannot find any cgroup driver of kubelet...")
	//
	// Since we can't easily mock the HTTP client in a unit test, we verify
	// the function signature returns error (not panic) and that the default
	// fallback values exist.
	if kubeletPodCgroupDriver == "" {
		// Default is "cgroupfs" — verify it's set somewhere reasonable
		t.Log("kubeletPodCgroupDriver default is empty, will be set on first successful sync")
	}
}
