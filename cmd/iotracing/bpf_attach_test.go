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

type infoErrorReader struct {
	closeCalls int
}

func (*infoErrorReader) ReadInto(any) error {
	return errors.New("unexpected read")
}

func (*infoErrorReader) ReadBatch(func() any) (bpf.PerfEventBatch, error) {
	return bpf.PerfEventBatch{}, errors.New("unexpected read")
}

func (r *infoErrorReader) Close() error {
	r.closeCalls++
	return nil
}

type infoErrorBPF struct {
	bpf.BPF
	reader      bpf.PerfEventReader
	infoErr     error
	attachCalls int
}

func (b *infoErrorBPF) EventPipeByName(context.Context, string, uint32) (bpf.PerfEventReader, error) {
	return b.reader, nil
}

func (b *infoErrorBPF) Info() (*bpf.Info, error) {
	return nil, b.infoErr
}

func (b *infoErrorBPF) AttachWithOptions([]bpf.AttachOption) error {
	b.attachCalls++
	return nil
}

func TestAttachAndEventPipeClosesReaderOnInfoError(t *testing.T) {
	wantErr := errors.New("BPF object closed")
	reader := &infoErrorReader{}
	obj := &infoErrorBPF{reader: reader, infoErr: wantErr}

	gotReader, err := attachAndEventPipe(t.Context(), obj)
	if !errors.Is(err, wantErr) {
		t.Fatalf("attachAndEventPipe() error = %v, want %v", err, wantErr)
	}
	if gotReader != nil {
		t.Fatalf("attachAndEventPipe() reader = %v, want nil", gotReader)
	}
	if reader.closeCalls != 1 {
		t.Errorf("reader close calls = %d, want 1", reader.closeCalls)
	}
	if obj.attachCalls != 0 {
		t.Errorf("AttachWithOptions calls = %d, want 0", obj.attachCalls)
	}
}
