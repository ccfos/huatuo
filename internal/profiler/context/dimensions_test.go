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

package context

import (
	"reflect"
	"testing"
)

func TestNormalizeScopeAliases(t *testing.T) {
	tests := map[string]string{
		"thread":        ScopePID,
		"pid":           ScopePID,
		"thread-group":  ScopeTGID,
		"tgid":          ScopeTGID,
		"cgroup":        ScopeCgroup,
		"process-group": ScopeProcessGroup,
	}
	for input, want := range tests {
		got, err := NormalizeScope(input)
		if err != nil || got != want {
			t.Errorf("NormalizeScope(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestNormalizeRequestedScopePreservesLegacyPIDDefault(t *testing.T) {
	tests := []struct {
		name     string
		scope    string
		explicit bool
		pid      int
		want     string
	}{
		{name: "implicit thread with pid", scope: "thread", pid: 42, want: ScopeTGID},
		{name: "explicit thread with pid", scope: "thread", explicit: true, pid: 42, want: ScopePID},
		{name: "implicit thread without pid", scope: "thread", want: ScopePID},
		{name: "explicit tgid", scope: "tgid", explicit: true, pid: 42, want: ScopeTGID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRequestedScope(tt.scope, tt.explicit, tt.pid)
			if err != nil || got != tt.want {
				t.Fatalf("normalizeRequestedScope() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestInferImplicitScopeFromTarget(t *testing.T) {
	tests := []struct {
		name           string
		explicit       bool
		pid            int
		processGroupID int
		cgroupID       uint64
		containerID    string
		want           string
	}{
		{name: "no target", want: ScopeAll},
		{name: "pid", pid: 42, want: ScopeTGID},
		{name: "process group", processGroupID: 7, want: ScopeProcessGroup},
		{name: "cgroup", cgroupID: 99, want: ScopeCgroup},
		{name: "container", containerID: "container-1", want: ScopeCgroup},
		{name: "explicit scope wins", explicit: true, processGroupID: 7, want: ScopePID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := ScopePID
			if tt.pid != 0 && !tt.explicit {
				scope = ScopeTGID
			}
			got := inferImplicitScope(scope, tt.explicit, tt.pid, tt.processGroupID, tt.cgroupID, tt.containerID)
			if got != tt.want {
				t.Fatalf("inferImplicitScope() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLockTypes(t *testing.T) {
	got, err := ParseLockTypes("mutex, spinlock,mutex,rwlock")
	if err != nil {
		t.Fatalf("ParseLockTypes() error = %v", err)
	}
	want := []string{"mutex", "spinlock", "rwlock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLockTypes() = %v, want %v", got, want)
	}
	if _, err := ParseLockTypes("semaphore"); err == nil {
		t.Fatal("ParseLockTypes(semaphore) error = nil")
	}
}

func TestParseProfileLabelsPreservesCompatibleValues(t *testing.T) {
	got, err := parseProfileLabels([]string{"service=checkout", "selector=zone=a,b"})
	if err != nil {
		t.Fatalf("parseProfileLabels() error = %v", err)
	}
	want := map[string]string{"service": "checkout", "selector": "zone=a,b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProfileLabels() = %#v, want %#v", got, want)
	}
	if _, err := parseProfileLabels([]string{"missing-value-separator"}); err == nil {
		t.Fatal("parseProfileLabels() error = nil for malformed label")
	}
}

func TestFormatPIDsPreservesJavaMultiPIDTargets(t *testing.T) {
	if got := formatPIDs([]int{42, 99}); got != "42,99" {
		t.Fatalf("formatPIDs() = %q, want %q", got, "42,99")
	}
}

func TestFormatCPUIdsPreservesSelectedCPUSet(t *testing.T) {
	if got := formatCPUIds([]int{0, 3}); got != "0,3" {
		t.Fatalf("formatCPUIds() = %q, want %q", got, "0,3")
	}
}

func TestSupportsNativeCPUFilter(t *testing.T) {
	for _, language := range []string{"c", "c++", "go"} {
		if !supportsNativeCPUFilter(language) {
			t.Errorf("supportsNativeCPUFilter(%q) = false", language)
		}
	}
	for _, language := range []string{"java", "python", ""} {
		if supportsNativeCPUFilter(language) {
			t.Errorf("supportsNativeCPUFilter(%q) = true", language)
		}
	}
}
