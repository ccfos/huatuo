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

//nolint:errname
const (
	Success           Return = iota // 0: Success
	_Reserved1                      // 1: Reserved
	_Reserved2                      // 2: Reserved
	ErrorNotSupported               // 3: Operation not supported
)

// String returns the string representation of a Return.
func (r Return) String() string {
	return errorStringFunc(r)
}

// errorStringFunc can be assigned if the system metax-sml library is in use.
var errorStringFunc = defaultErrorStringFunc

// defaultErrorStringFunc provides a basic implementation for Return string representation.
var defaultErrorStringFunc = func(r Return) string {
	return mxSmlGetErrorString(r)
}
