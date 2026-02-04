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
)

type Error struct {
	symbol string
	code   Return
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed: %s", e.symbol, e.code.String())
}

// IsNotSupported reports whether err represents an unsupported operation.
func IsNotSupported(err error) bool {
	var smlErr *Error
	return errors.As(err, &smlErr) &&
		smlErr.code == ErrorNotSupported
}

// checkReturnCode converts a return code to an error.
func checkReturnCode(symbol string, code Return) error {
	if code == Success {
		return nil
	}

	return &Error{
		symbol: symbol,
		code:   code,
	}
}
