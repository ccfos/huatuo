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

package bpf

import (
	"errors"
	"sync"
)

// Reference keeps a published BPF object alive while acquired leases exist.
// Its zero value is ready for use. A Reference must not be copied after use,
// and Publish and UnPublish calls must be serialized.
type Reference struct {
	mu          sync.Mutex
	object      BPF
	readers     int
	readersDone chan struct{}
}

// Lease keeps a BPF object alive until Release is called. It must not be
// copied.
type Lease struct {
	BPF
	reference *Reference
}

// Publish makes object available to Acquire calls and transfers its ownership
// to the reference.
func (r *Reference) Publish(object BPF) error {
	if object == nil {
		return errors.New("bpf: cannot publish a nil object")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.object != nil {
		return errors.New("bpf: an object is already published")
	}

	r.object = object
	return nil
}

// Acquire returns a lease for the published object.
func (r *Reference) Acquire() (Lease, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.object == nil {
		return Lease{}, false
	}

	r.readers++
	return Lease{
		BPF:       r.object,
		reference: r,
	}, true
}

// Release ends the lease and allows UnPublish to close the object.
func (l *Lease) Release() {
	if l == nil || l.reference == nil {
		return
	}

	reference := l.reference
	l.reference = nil

	reference.mu.Lock()
	defer reference.mu.Unlock()

	reference.readers--
	if reference.readers == 0 && reference.readersDone != nil {
		close(reference.readersDone)
		reference.readersDone = nil
	}
}

// UnPublish prevents new leases, waits for current leases, and closes the
// object.
func (r *Reference) UnPublish() error {
	r.mu.Lock()

	object := r.object
	r.object = nil
	if object == nil {
		r.mu.Unlock()
		return nil
	}

	if r.readers == 0 {
		r.mu.Unlock()
		return object.Close()
	}

	r.readersDone = make(chan struct{})
	readersDone := r.readersDone
	r.mu.Unlock()

	<-readersDone
	return object.Close()
}
