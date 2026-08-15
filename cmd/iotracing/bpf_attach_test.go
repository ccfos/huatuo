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

package main

import (
	"context"
	"errors"
	"testing"

	"huatuo-bamai/internal/bpf"
)

type closeTrackingReader struct {
	bpf.PerfEventReader
	closeCalls int
}

func (r *closeTrackingReader) Close() error {
	r.closeCalls++
	return nil
}

type infoFailingBPF struct {
	bpf.BPF
	reader      bpf.PerfEventReader
	infoErr     error
	attachCalls int
}

func (b *infoFailingBPF) EventPipeByName(context.Context, string, uint32) (bpf.PerfEventReader, error) {
	return b.reader, nil
}

func (b *infoFailingBPF) Info() (*bpf.Info, error) {
	return nil, b.infoErr
}

func (b *infoFailingBPF) AttachWithOptions([]bpf.AttachOption) error {
	b.attachCalls++
	return nil
}

func TestAttachAndEventPipeClosesReaderWhenInfoFails(t *testing.T) {
	infoErr := errors.New("BPF object closed")
	reader := &closeTrackingReader{}
	b := &infoFailingBPF{reader: reader, infoErr: infoErr}

	gotReader, err := attachAndEventPipe(t.Context(), b)

	if !errors.Is(err, infoErr) {
		t.Fatalf("attachAndEventPipe() error = %v, want wrapped %v", err, infoErr)
	}
	if gotReader != nil {
		t.Fatalf("attachAndEventPipe() reader = %v, want nil", gotReader)
	}
	if reader.closeCalls != 1 {
		t.Fatalf("reader Close() calls = %d, want 1", reader.closeCalls)
	}
	if b.attachCalls != 0 {
		t.Fatalf("AttachWithOptions() calls = %d, want 0", b.attachCalls)
	}
}
