// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The MetaX Authors
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

package sml

import (
	"errors"
	"fmt"
	"testing"
)

func TestReturnStringUsesInjectedResolver(t *testing.T) {
	original := errorStringFunc
	t.Cleanup(func() { errorStringFunc = original })

	errorStringFunc = func(r Return) string {
		return fmt.Sprintf("resolved:%d", int32(r))
	}

	for _, code := range []Return{Success, ErrorNotSupported} {
		t.Run(fmt.Sprintf("code_%d", int32(code)), func(t *testing.T) {
			want := fmt.Sprintf("resolved:%d", int32(code))
			if got := code.String(); got != want {
				t.Fatalf("Return(%d).String() = %q, want %q", int32(code), got, want)
			}
		})
	}
}

func TestCheckReturnCode(t *testing.T) {
	original := errorStringFunc
	t.Cleanup(func() { errorStringFunc = original })
	errorStringFunc = func(Return) string { return "Operation not supported" }

	if err := checkReturnCode("mxSmlInit", Success); err != nil {
		t.Fatalf("checkReturnCode(Success) = %v, want nil", err)
	}

	err := checkReturnCode("mxSmlGetDeviceCount", ErrorNotSupported)
	if err == nil {
		t.Fatal("checkReturnCode(ErrorNotSupported) = nil, want error")
	}

	var smlErr *Error
	if !errors.As(err, &smlErr) {
		t.Fatalf("checkReturnCode error type = %T, want *Error", err)
	}

	if smlErr.symbol != "mxSmlGetDeviceCount" || smlErr.code != ErrorNotSupported {
		t.Fatalf("checkReturnCode symbol/code = %s/%d, want mxSmlGetDeviceCount/3",
			smlErr.symbol, smlErr.code)
	}

	if err.Error() == "" {
		t.Fatal("Error() = \"\", want non-empty string")
	}
}

func TestErrorString(t *testing.T) {
	original := errorStringFunc
	t.Cleanup(func() { errorStringFunc = original })
	errorStringFunc = func(Return) string { return "Operation not supported" }

	err := &Error{symbol: "mxSmlGetDeviceInfo", code: ErrorNotSupported}
	want := "mxSmlGetDeviceInfo failed: Operation not supported"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestIsNotSupported(t *testing.T) {
	original := errorStringFunc
	t.Cleanup(func() { errorStringFunc = original })
	errorStringFunc = func(Return) string { return "Operation not supported" }

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("boom"),
			want: false,
		},
		{
			name: "not supported",
			err:  &Error{code: ErrorNotSupported},
			want: true,
		},
		{
			name: "success",
			err:  &Error{code: Success},
			want: false,
		},
		{
			name: "reserved",
			err:  &Error{code: _Reserved1},
			want: false,
		},
		{
			name: "wrapped not supported",
			err:  fmt.Errorf("query device: %w", &Error{code: ErrorNotSupported}),
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

	r.IncOnNoError(errors.New("open failed"))
	if got := r; got != 0 {
		t.Fatalf("refcount after failed increment = %d, want 0", got)
	}

	if got := func() refcount {
		r.IncOnNoError(nil)
		return r
	}(); got != 1 {
		t.Fatalf("refcount after first successful increment = %d, want 1", got)
	}

	if got := func() refcount {
		r.IncOnNoError(nil)
		return r
	}(); got != 2 {
		t.Fatalf("refcount after second successful increment = %d, want 2", got)
	}

	if got := func() refcount {
		r.DecOnNoError(errors.New("close failed"))
		return r
	}(); got != 2 {
		t.Fatalf("refcount after failed decrement = %d, want 2", got)
	}

	if got := func() refcount {
		r.DecOnNoError(nil)
		return r
	}(); got != 1 {
		t.Fatalf("refcount after first successful decrement = %d, want 1", got)
	}

	if got := func() refcount {
		r.DecOnNoError(nil)
		return r
	}(); got != 0 {
		t.Fatalf("refcount after second successful decrement = %d, want 0", got)
	}

	if got := func() refcount {
		r.DecOnNoError(nil)
		return r
	}(); got != 0 {
		t.Fatalf("refcount after decrement at zero = %d, want 0", got)
	}
}

func TestRefcountDoesNotUnderflow(t *testing.T) {
	var r refcount

	if got := func() refcount {
		r.DecOnNoError(nil)
		return r
	}(); got != 0 {
		t.Fatalf("refcount after decrement at zero = %d, want 0", got)
	}

	if got := func() refcount {
		r.IncOnNoError(nil)
		return r
	}(); got != 1 {
		t.Fatalf("refcount after increment from zero = %d, want 1", got)
	}

	if got := func() refcount {
		r.DecOnNoError(nil)
		return r
	}(); got != 0 {
		t.Fatalf("refcount after first decrement = %d, want 0", got)
	}

	if got := func() refcount {
		r.DecOnNoError(nil)
		return r
	}(); got != 0 {
		t.Fatalf("refcount after extra decrement = %d, want 0", got)
	}
}

func TestRefcountDecKeepsValueOnNonNilError(t *testing.T) {
	var r refcount

	if got := func() refcount {
		r.IncOnNoError(nil)
		return r
	}(); got != 1 {
		t.Fatalf("refcount after increment = %d, want 1", got)
	}

	if got := func() refcount {
		r.IncOnNoError(nil)
		return r
	}(); got != 2 {
		t.Fatalf("refcount after second increment = %d, want 2", got)
	}

	if got := func() refcount {
		r.DecOnNoError(errors.New("stop failed"))
		return r
	}(); got != 2 {
		t.Fatalf("refcount after failed decrement = %d, want 2 (unchanged)", got)
	}
}
