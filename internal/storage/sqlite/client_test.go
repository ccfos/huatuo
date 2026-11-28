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

package sqlite

import "testing"

func TestIsMemoryDSN(t *testing.T) {
	tests := []struct {
		dsn  string
		want bool
	}{
		{dsn: ":memory:", want: true},
		{dsn: "file::memory:?cache=shared", want: true},
		{dsn: "file:memdb1?mode=memory&cache=shared", want: true},
		{dsn: "file:memdb1?cache=shared&mode=memory", want: true},
		{dsn: "storage.db", want: false},
		{dsn: "file:storage.db?mode=rwc", want: false},
		{dsn: "file:memdb1?mode=memory-mapped", want: false},
	}

	for _, test := range tests {
		t.Run(test.dsn, func(t *testing.T) {
			if got := isMemoryDSN(test.dsn); got != test.want {
				t.Fatalf("isMemoryDSN(%q) = %t, want %t", test.dsn, got, test.want)
			}
		})
	}
}
