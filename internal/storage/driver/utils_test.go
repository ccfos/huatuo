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

package driver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestDriverValueUtilities(t *testing.T) {
	timestamp := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	tests := []struct {
		name    string
		input   any
		want    []any
		wantErr error
	}{
		{name: "normalizes times in slices", input: []time.Time{timestamp}, want: []any{"2026-07-23 04:00:00.000 +0000"}},
		{name: "keeps scalar slice values", input: []string{"a", "b"}, want: []any{"a", "b"}},
		{name: "rejects scalar", input: "a", wantErr: ErrInRequiresSlice},
		{name: "rejects empty slice", input: []string{}, wantErr: ErrInRequiresNonEmpty},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FlattenInValues(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("FlattenInValues() error = %v, want %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("FlattenInValues() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	if WithContext(nil) == nil {
		t.Error("WithContext(nil) returned nil")
	}
	ctx := context.WithValue(context.Background(), "key", "value")
	if WithContext(ctx) != ctx {
		t.Error("WithContext(ctx) did not preserve the supplied context")
	}

	data := []byte{1, 2, 3}
	clone := CloneBytes(data)
	clone[0] = 9
	if data[0] == clone[0] {
		t.Error("CloneBytes() returned an aliased slice")
	}
	if CloneBytes(nil) != nil {
		t.Error("CloneBytes(nil) did not return nil")
	}
}

func TestStringValue(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{name: "nil", want: ""},
		{name: "string", input: "value", want: "value"},
		{name: "bytes", input: []byte("value"), want: "value"},
		{name: "number", input: 42, want: "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StringValue(tt.input); got != tt.want {
				t.Errorf("StringValue(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
