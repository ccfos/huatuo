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

package request

import (
	"errors"
	"testing"
)

func TestRawHTTPReadResponseWithoutConnection(t *testing.T) {
	tests := []struct {
		name string
		raw  *RawHTTP
	}{
		{name: "nil receiver"},
		{name: "unconnected client", raw: &RawHTTP{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := tt.raw.ReadResponse()
			if response != nil {
				t.Fatalf("ReadResponse() response = %v, want nil", response)
			}
			if !errors.Is(err, errRawHTTPNotConnected) {
				t.Fatalf("ReadResponse() error = %v, want %v", err, errRawHTTPNotConnected)
			}
		})
	}
}
