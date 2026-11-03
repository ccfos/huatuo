// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package dcmi

import (
	"errors"
	"fmt"
	"testing"
)

func TestReturnString(t *testing.T) {
	for _, tc := range []struct {
		code Return
		want string
	}{
		{code: Success, want: "unknown error code: 0"},
		{code: -8001, want: "The input parameter is incorrect"},
		{code: -8008, want: "The device does not exist"},
		{code: -8255, want: "Device ID/function is not supported"},
		{code: -99999, want: "dcmi libdcmi.so failed to load"},
		{code: 1, want: "unknown error code: 1"},
		{code: -12345, want: "unknown error code: -12345"},
	} {
		t.Run(fmt.Sprintf("code_%d", int32(tc.code)), func(t *testing.T) {
			if got := tc.code.String(); got != tc.want {
				t.Fatalf("Return(%d).String() = %q, want %q", int32(tc.code), got, tc.want)
			}
		})
	}
}

func TestCheckReturnCode(t *testing.T) {
	if err := checkReturnCode("dcmi_init", Success); err != nil {
		t.Fatalf("checkReturnCode(Success) = %v, want nil", err)
	}

	err := checkReturnCode("dcmi_get_card_list", -8001)
	if err == nil {
		t.Fatal("checkReturnCode(-8001) = nil, want error")
	}

	var dcmiErr *Error
	if !errors.As(err, &dcmiErr) {
		t.Fatalf("checkReturnCode error type = %T, want *Error", err)
	}
	if dcmiErr.symbol != "dcmi_get_card_list" || dcmiErr.code != -8001 {
		t.Fatalf("checkReturnCode error = %+v, want symbol and code preserved", dcmiErr)
	}
}

func TestErrorString(t *testing.T) {
	err := &Error{symbol: "dcmi_get_device_health", code: -8008}
	want := "dcmi_get_device_health failed: The device does not exist"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestIsNotSupported(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unrelated error", err: errors.New("boom"), want: false},
		{name: "device function unsupported", err: &Error{code: -8255}, want: true},
		{name: "container unsupported", err: &Error{code: -8013}, want: true},
		{name: "function missing", err: &Error{code: -99998}, want: true},
		{name: "plain success", err: &Error{code: Success}, want: false},
		{name: "other failure", err: &Error{code: -8005}, want: false},
		{
			name: "wrapped unsupported",
			err:  fmt.Errorf("query device: %w", &Error{code: -8255}),
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotSupported(tc.err); got != tc.want {
				t.Fatalf("IsNotSupported(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRefcountOnlyTracksSuccessfulCalls(t *testing.T) {
	var r refcount

	if got := r; got != 0 {
		t.Fatalf("zero value = %d, want 0", got)
	}

	r.incOnNoError(errors.New("open failed"))
	if got := r; got != 0 {
		t.Fatalf("refcount after failed increment = %d, want 0", got)
	}

	if got := func() refcount { r.incOnNoError(nil); return r }(); got != 1 {
		t.Fatalf("refcount after first successful increment = %d, want 1", got)
	}

	if got := func() refcount { r.incOnNoError(nil); return r }(); got != 2 {
		t.Fatalf("refcount after second successful increment = %d, want 2", got)
	}

	if got := func() refcount { r.decOnNoError(errors.New("close failed")); return r }(); got != 2 {
		t.Fatalf("refcount after failed decrement = %d, want 2", got)
	}

	if got := func() refcount { r.decOnNoError(nil); return r }(); got != 1 {
		t.Fatalf("refcount after first successful decrement = %d, want 1", got)
	}

	if got := func() refcount { r.decOnNoError(nil); return r }(); got != 0 {
		t.Fatalf("refcount after second successful decrement = %d, want 0", got)
	}

	if got := func() refcount { r.decOnNoError(nil); return r }(); got != 0 {
		t.Fatalf("refcount after decrement at zero = %d, want 0", got)
	}
}

func TestRefcountDoesNotUnderflow(t *testing.T) {
	var r refcount

	if got := func() refcount { r.decOnNoError(nil); return r }(); got != 0 {
		t.Fatalf("refcount after decrement at zero = %d, want 0", got)
	}

	if got := func() refcount { r.incOnNoError(nil); return r }(); got != 1 {
		t.Fatalf("refcount after increment from zero = %d, want 1", got)
	}

	if got := func() refcount { r.decOnNoError(nil); return r }(); got != 0 {
		t.Fatalf("refcount after first decrement = %d, want 0", got)
	}

	if got := func() refcount { r.decOnNoError(nil); return r }(); got != 0 {
		t.Fatalf("refcount after extra decrement = %d, want 0", got)
	}
}

func TestRefcountDecKeepsValueOnNonNilError(t *testing.T) {
	var r refcount

	if got := func() refcount { r.incOnNoError(nil); return r }(); got != 1 {
		t.Fatalf("refcount after increment = %d, want 1", got)
	}

	if got := func() refcount { r.incOnNoError(nil); return r }(); got != 2 {
		t.Fatalf("refcount after second increment = %d, want 2", got)
	}

	if got := func() refcount { r.decOnNoError(errors.New("stop failed")); return r }(); got != 2 {
		t.Fatalf("refcount after failed decrement = %d, want 2 (unchanged)", got)
	}
}
