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

package job

import (
	"errors"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

var (
	// ErrNotFound indicates that a requested Job does not exist.
	ErrNotFound = driver.ErrNotFound
	// ErrAlreadyExists indicates that a Job ID is already persisted.
	ErrAlreadyExists = driver.ErrAlreadyExists

	// ErrQuotaExceeded indicates that active Job capacity is exhausted.
	ErrQuotaExceeded = errors.New("job quota exceeded")
	// ErrUnsupportedKind indicates that no typed Node service owns the Job.
	ErrUnsupportedKind = errors.New("unsupported job kind")
	// ErrPersistence indicates that a durable Job transition failed.
	ErrPersistence = errors.New("job persistence failed")
	// ErrConflict indicates that the persisted Job state changed concurrently.
	ErrConflict = driver.ErrConflict
	// ErrInvalidQuery indicates invalid Job query or creation parameters.
	ErrInvalidQuery = errors.New("invalid job query")
	// ErrShuttingDown indicates that the Manager no longer accepts new Jobs.
	ErrShuttingDown = errors.New("job manager is shutting down")
	// ErrJobTerminal indicates that a command cannot change a terminal Job.
	ErrJobTerminal = errors.New("job is already terminal")
	// ErrJobNotSupervised indicates that an active Job has no local supervisor.
	ErrJobNotSupervised = errors.New("job is not supervised")
)
