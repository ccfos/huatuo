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

package memray

import "testing"

func TestResolveNativeSymbolPID(t *testing.T) {
	const (
		headerPID = int32(1)
		hostPID   = int32(12345)
	)

	if got := resolveNativeSymbolPID(Options{}, headerPID); got != headerPID {
		t.Fatalf("default symbol PID = %d, want %d", got, headerPID)
	}
	if got := resolveNativeSymbolPID(
		Options{NativeSymbolPID: hostPID},
		headerPID,
	); got != hostPID {
		t.Fatalf("overridden symbol PID = %d, want %d", got, hostPID)
	}
}
