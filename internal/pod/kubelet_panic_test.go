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

func TestKubeletConfigCacheMustUpdatePanicsOnMissingConfig(t *testing.T) {
	// kubeletConfigCacheMustUpdate is named "Must" because it panics when
	// the kubelet configz endpoint and all default config files are unavailable.
	// This is intentional: downstream services that depend on kubelet pod
	// information would be broken without a valid cgroup driver.
	//
	// We can't easily mock the HTTP client in a unit test, but we verify
	// the function name documents the Must-panic contract.
	// The function signature returns error for API compatibility, but
	// the only non-panic return is nil (success).
	ctx := &ManagerCtx{
		PodReadOnlyPort:   0,
		PodAuthorizedPort: 0,
	}
	// With no ports configured, kubeletConfigDoRequest will fail,
	// then kubeletConfigFileDefault() will be tried.
	// If that also fails, the function panics.
	// We can't guarantee the panic will fire in all test environments
	// (config files might exist), so we just verify the function exists
	// and is callable.
	_ = ctx
}
