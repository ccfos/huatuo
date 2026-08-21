// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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

package mtml

import (
	"errors"
	"fmt"
)

// Error wraps an MTML return code with the symbol that produced it.
type Error struct {
	symbol string
	code   Return
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed: %s", e.symbol, e.code.String())
}

// IsNotSupported reports whether err means the operation is not supported by
// the current device/driver.
func IsNotSupported(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.code == ErrorNotSupported
}

// checkReturnCode converts a non-success return code into an *Error.
func checkReturnCode(symbol string, code Return) error {
	if code == Success {
		return nil
	}
	return &Error{symbol: symbol, code: code}
}

// errNotSupportedSymbol returns a NotSupported error for an optional symbol
// whose C function pointer was not resolved (symbol absent from libmtml.so).
func errNotSupportedSymbol(symbol string) error {
	return &Error{symbol: symbol, code: ErrorNotSupported}
}

// String returns a human-readable description of a Return code.
//
// It uses a hardcoded switch table only; the library's own
// mtmlErrorString is intentionally NOT called from Go code so that
// *Error formatting has no dependency on libmtml.so being mapped.
// This is the prerequisite for safely dlclose'ing the .so in Shutdown.
func (r Return) String() string {
	switch r {
	case Success:
		return "success"
	case ErrorDriverNotLoaded:
		return "driver not loaded"
	case ErrorDriverFailure:
		return "driver failure"
	case ErrorInvalidArgument:
		return "invalid argument"
	case ErrorNotSupported:
		return "not supported"
	case ErrorNoPermission:
		return "no permission"
	case ErrorInsufficientSize:
		return "insufficient size"
	case ErrorNotFound:
		return "not found"
	case ErrorInsufficientMemory:
		return "insufficient memory"
	case ErrorDriverTooOld:
		return "driver too old"
	case ErrorDriverTooNew:
		return "driver too new"
	case ErrorTimeout:
		return "timeout"
	case ErrorResourceIsBusy:
		return "resource is busy"
	case ErrorUnknown:
		return "unknown"
	}
	return fmt.Sprintf("mtml error code %d", int32(r))
}
