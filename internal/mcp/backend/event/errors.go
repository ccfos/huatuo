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

package event

// RetryableError is a storage-neutral error classification. Native database
// adapters can classify driver exceptions without inventing HTTP statuses.
type RetryableError interface {
	error
	Retryable() bool
}

type ReadError struct {
	Err       error
	Transient bool
}

func (e *ReadError) Error() string   { return e.Err.Error() }
func (e *ReadError) Unwrap() error   { return e.Err }
func (e *ReadError) Retryable() bool { return e.Transient }
