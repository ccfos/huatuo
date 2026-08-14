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

package profiler

import "testing"

func TestJavaMainClass(t *testing.T) {
	tests := []struct {
		name    string
		cmdline []string
		want    string
	}{
		{name: "long class path", cmdline: []string{"java", "--class-path", "libs/*", "com.example.Main"}, want: "com.example.Main"},
		{name: "short class path", cmdline: []string{"java", "-cp", "libs/*", "com.example.Main"}, want: "com.example.Main"},
		{name: "legacy class path", cmdline: []string{"java", "-classpath", "libs/*", "com.example.Main"}, want: "com.example.Main"},
		{name: "jar", cmdline: []string{"java", "-jar", "app.jar", "arg"}, want: "app.jar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := javaMainClass(tt.cmdline); got != tt.want {
				t.Errorf("javaMainClass() = %q, want %q", got, tt.want)
			}
		})
	}
}
