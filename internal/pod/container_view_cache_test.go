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
	"testing"
)

func TestContainersByTypeQosCachedUntilCommit(t *testing.T) {
	// containersByTypeQos operates on the package-level store; seed and
	// restore it around the assertions.
	savedRecords := containerView.records
	savedViews := containerView.views
	savedErr := containerView.err
	savedActive := containerView.isActive
	t.Cleanup(func() {
		containerView.records = savedRecords
		containerView.views = savedViews
		containerView.err = savedErr
		containerView.isActive = savedActive
	})

	containerView.isActive = true
	containerView.err = nil
	containerView.records = map[string]*containerRecord{
		"a": {container: &Container{ID: "a", Type: ContainerTypeNormal, Qos: containerQosGuaranteed}},
		"b": {container: &Container{ID: "b", Type: ContainerTypeSidecar, Qos: ContainerQosLevelMin}},
	}
	containerView.views = nil

	normal, err := containersByTypeQos(ContainerTypeNormal, ContainerQosLevelMin)
	if err != nil {
		t.Fatal(err)
	}
	if len(normal) != 1 || normal["a"] == nil {
		t.Fatalf("normal view: got %v", normal)
	}

	key := containerViewKey{typeMask: ContainerTypeNormal, minLevel: ContainerQosLevelMin}
	if containerView.views[key] == nil || len(containerView.views[key].containers) != 1 {
		t.Fatal("expected the normal view to be cached")
	}

	all, err := containersByTypeQos(ContainerTypeAll, ContainerQosLevelMin)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all view: got %v", all)
	}

	// A commit must drop cached views so later readers observe new membership.
	delete(containerView.records, "a")
	containerView.commit(containerSnapshot{records: map[string]*containerRecord{
		"b": containerView.records["b"],
	}, isComplete: true}, nil)

	after, err := containersByTypeQos(ContainerTypeNormal, ContainerQosLevelMin)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("stale cache after commit: got %v", after)
	}
}

func TestNormalContainersWithHost(t *testing.T) {
	savedRecords := containerView.records
	savedViews := containerView.views
	savedErr := containerView.err
	savedActive := containerView.isActive
	t.Cleanup(func() {
		containerView.records = savedRecords
		containerView.views = savedViews
		containerView.err = savedErr
		containerView.isActive = savedActive
	})

	containerView.isActive = true
	containerView.err = nil
	containerView.records = map[string]*containerRecord{
		"a": {container: &Container{ID: "a", Type: ContainerTypeNormal, Qos: containerQosGuaranteed}},
	}
	containerView.views = nil

	// Mutating the WithHost view must not corrupt the shared cached view.
	withHost, err := NormalContainersWithHost()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withHost[""]; !ok {
		t.Fatal("expected the host entry under the empty key")
	}
	if withHost[""] != nil {
		t.Fatal("expected the host entry to be nil")
	}
	if len(withHost) != 2 || withHost["a"] == nil {
		t.Fatalf("unexpected view: %v", withHost)
	}

	shared, err := NormalContainers()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shared[""]; ok {
		t.Fatal("shared view must not contain the host entry")
	}
	if len(shared) != 1 {
		t.Fatalf("shared view must be untouched: %v", shared)
	}
}
