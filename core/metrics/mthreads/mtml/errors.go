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

// IsRecoverable reports whether err — or any error in an errors.Join
// tree rooted at err — indicates a transient driver/library state
// that a Shutdown+Init cycle may fix. These codes are typically
// returned after a driver upgrade, device hot-plug, or when the
// driver state has been torn down externally (e.g. modprobe -r
// mthreads).
func IsRecoverable(err error) bool {
	return hasRecoverableCode(err)
}

// hasRecoverableCode walks the error tree rooted at err and reports
// whether any leaf is an *Error with a recoverable return code.
func hasRecoverableCode(err error) bool {
	if err == nil {
		return false
	}
	if joiner, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joiner.Unwrap() {
			if hasRecoverableCode(e) {
				return true
			}
		}
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		switch e.code {
		case ErrorDriverNotLoaded, ErrorDriverFailure,
			ErrorResourceIsBusy, ErrorTimeout:
			return true
		}
	}
	return false
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

// MakeError constructs an *Error for the given symbol and code. It is
// exported for test-only use (so callers and tests can build recognizable
// errors without re-implementing the unexported type). Production code
// paths should use checkReturnCode and errNotSupportedSymbol directly.
func MakeError(symbol string, code Return) error {
	return &Error{symbol: symbol, code: code}
}

// errNotReady is returned by native API entry points when the library
// is in a not-ready state (fresh process, post-Shutdown, or post-failed-
// Init).
var errNotReady = &notReadyError{}

type notReadyError struct{}

func (e *notReadyError) Error() string {
	return "mtml library is not ready (Init has not succeeded or Shutdown was called)"
}

// IsNotReady reports whether err is the not-ready sentinel returned
// by native entry points when the library has not been successfully
// initialized.
func IsNotReady(err error) bool {
	var e *notReadyError
	return errors.As(err, &e)
}

// String returns a human-readable description of a Return code.
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
