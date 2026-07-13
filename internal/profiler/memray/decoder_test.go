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

package memray

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameTreeGetTraceIndexDistinguishesInstrOffset(t *testing.T) {
	var ft frameTree

	first := pythonFrameKey{CodeObjectID: 7, InstrOffset: 10, IsEntry: true}
	second := pythonFrameKey{CodeObjectID: 7, InstrOffset: 20, IsEntry: true}

	firstIdx := ft.getTraceIndex(0, first)
	secondIdx := ft.getTraceIndex(0, second)

	if firstIdx == secondIdx {
		t.Fatalf("getTraceIndex() returned same index %d for different instruction offsets", firstIdx)
	}

	if got := ft.nodes[firstIdx].Frame; got != first {
		t.Fatalf("first frame = %#v, want %#v", got, first)
	}

	if got := ft.nodes[secondIdx].Frame; got != second {
		t.Fatalf("second frame = %#v, want %#v", got, second)
	}
}

func TestFrameTreeGetTraceIndexReusesSameFrameKey(t *testing.T) {
	var ft frameTree

	frame := pythonFrameKey{CodeObjectID: 11, InstrOffset: 32, IsEntry: false}

	firstIdx := ft.getTraceIndex(0, frame)
	secondIdx := ft.getTraceIndex(0, frame)

	if firstIdx != secondIdx {
		t.Fatalf("getTraceIndex() = (%d, %d), want same index", firstIdx, secondIdx)
	}

	if got := len(ft.nodes); got != 2 {
		t.Fatalf("node count = %d, want 2", got)
	}
}

func TestHandleCodeObjectStoresMetadata(t *testing.T) {
	lineTable := []byte{0x01, 0x02, 0x03}
	rd := &reader{
		r:           bytes.NewReader(encodeCodeObjectRecord(42, "alloc", "app.py", 128, lineTable)),
		codeObjects: make(map[uint64]codeObject),
	}

	if err := rd.handleCodeObject(); err != nil {
		t.Fatalf("handleCodeObject() error = %v", err)
	}

	got, ok := rd.codeObjects[42]
	if !ok {
		t.Fatal("code object 42 was not stored")
	}

	if got.Func != "alloc" {
		t.Fatalf("Func = %q, want %q", got.Func, "alloc")
	}
	if got.Filename != "app.py" {
		t.Fatalf("Filename = %q, want %q", got.Filename, "app.py")
	}
	if got.FirstLine != 128 {
		t.Fatalf("FirstLine = %d, want %d", got.FirstLine, 128)
	}
	if !bytes.Equal(got.LineTable, lineTable) {
		t.Fatalf("LineTable = %v, want %v", got.LineTable, lineTable)
	}
}

func TestHandleCodeObjectWithEmptyLineTable(t *testing.T) {
	rd := &reader{
		r:           bytes.NewReader(encodeCodeObjectRecord(7, "main", "main.py", 1, nil)),
		codeObjects: make(map[uint64]codeObject),
	}

	if err := rd.handleCodeObject(); err != nil {
		t.Fatalf("handleCodeObject() error = %v", err)
	}

	got, ok := rd.codeObjects[7]
	if !ok {
		t.Fatal("code object 7 was not stored")
	}

	if got.Func != "main" || got.Filename != "main.py" || got.FirstLine != 1 {
		t.Fatalf("stored code object = %#v, want Func=%q Filename=%q FirstLine=%d", got, "main", "main.py", 1)
	}

	if len(got.LineTable) != 0 {
		t.Fatalf("LineTable length = %d, want 0", len(got.LineTable))
	}
}

func TestHandleCodeObjectRejectsOversizedLineTable(t *testing.T) {
	var payload bytes.Buffer
	payload.Write(encodeVarint(7))
	payload.WriteString("main")
	payload.WriteByte(0)
	payload.WriteString("main.py")
	payload.WriteByte(0)
	payload.Write(encodeSignedVarint(1))
	payload.Write(encodeVarint(maxCodeObjectLineTableSize + 1))

	rd := &reader{
		r:           bytes.NewReader(payload.Bytes()),
		codeObjects: make(map[uint64]codeObject),
	}

	err := rd.handleCodeObject()
	if err == nil {
		t.Fatal("handleCodeObject() error = nil, want oversized line table error")
	}
	if got, want := err.Error(), "code object line table too large"; !bytes.Contains([]byte(got), []byte(want)) {
		t.Fatalf("handleCodeObject() error = %q, want substring %q", got, want)
	}
}

func TestHandleCodeObjectRestoresFirstLineFromDelta(t *testing.T) {
	var payload bytes.Buffer
	payload.Write(encodeCodeObjectRecord(1, "outer", "sample.py", 120, nil))
	payload.Write(encodeCodeObjectRecord(2, "inner", "sample.py", -4, nil))

	rd := &reader{
		r:           bytes.NewReader(payload.Bytes()),
		codeObjects: make(map[uint64]codeObject),
	}

	if err := rd.handleCodeObject(); err != nil {
		t.Fatalf("first handleCodeObject() error = %v", err)
	}
	if err := rd.handleCodeObject(); err != nil {
		t.Fatalf("second handleCodeObject() error = %v", err)
	}

	if got := rd.codeObjects[1].FirstLine; got != 120 {
		t.Fatalf("first code object FirstLine = %d, want 120", got)
	}
	if got := rd.codeObjects[2].FirstLine; got != 116 {
		t.Fatalf("second code object FirstLine = %d, want 116", got)
	}
}

func TestRenderPythonFrameLabelFallsBackToFuncName(t *testing.T) {
	rd := &reader{
		codeObjects: map[uint64]codeObject{
			3: {Func: "worker"},
		},
	}

	got := rd.renderPythonFrameLabel(pythonFrameKey{CodeObjectID: 3, InstrOffset: 6, IsEntry: true})
	if got != "worker" {
		t.Fatalf("renderPythonFrameLabel() = %q, want %q", got, "worker")
	}
}

func TestRenderPythonFrameLabelUsesLineNumberWhenAvailable(t *testing.T) {
	rd := &reader{
		header: Header{PythonVersion: 0x030A0000},
		codeObjects: map[uint64]codeObject{
			5: {
				Func:      "alloc",
				Filename:  "app.py",
				FirstLine: 120,
				LineTable: []byte{2, 3},
			},
		},
	}

	got := rd.renderPythonFrameLabel(pythonFrameKey{CodeObjectID: 5, InstrOffset: 1, IsEntry: true})
	if got != "alloc app.py:123" {
		t.Fatalf("renderPythonFrameLabel() = %q, want %q", got, "alloc app.py:123")
	}
}

func TestRenderPythonFrameLabelSanitizesFilenameForFoldedStacks(t *testing.T) {
	rd := &reader{
		header: Header{PythonVersion: 0x030A0000},
		opt:    Options{Separator: ";"},
		codeObjects: map[uint64]codeObject{
			5: {
				Func:      "alloc",
				Filename:  "dir;a\npp.py",
				FirstLine: 120,
				LineTable: []byte{2, 3},
			},
		},
	}

	got := rd.renderPythonFrameLabel(pythonFrameKey{CodeObjectID: 5, InstrOffset: 1, IsEntry: true})
	if got != "alloc dir_a pp.py:123" {
		t.Fatalf("renderPythonFrameLabel() = %q, want %q", got, "alloc dir_a pp.py:123")
	}
}

func TestPythonStackFramesUsesEnhancedLabels(t *testing.T) {
	rd := &reader{
		header: Header{PythonVersion: 0x030A0000},
		codeObjects: map[uint64]codeObject{
			5: {
				Func:      "alloc",
				Filename:  "app.py",
				FirstLine: 120,
				LineTable: []byte{2, 3},
			},
		},
		frameTree: frameTree{
			nodes: []frameNode{
				{},
				{
					Frame: pythonFrameKey{
						CodeObjectID: 5,
						InstrOffset:  1,
						IsEntry:      true,
					},
				},
			},
		},
	}

	frames, entries := rd.pythonStackFrames(1)
	if len(frames) != 1 || frames[0] != "alloc app.py:123" {
		t.Fatalf("pythonStackFrames() frames = %#v, want [\"alloc app.py:123\"]", frames)
	}
	if len(entries) != 1 || !entries[0] {
		t.Fatalf("pythonStackFrames() entries = %#v, want [true]", entries)
	}
}

func TestFrameTreeGetTraceIndexKeepsChildrenSorted(t *testing.T) {
	var ft frameTree

	frames := []pythonFrameKey{
		{CodeObjectID: 9, InstrOffset: 10, IsEntry: true},
		{CodeObjectID: 2, InstrOffset: 30, IsEntry: true},
		{CodeObjectID: 9, InstrOffset: 5, IsEntry: false},
	}

	for _, frame := range frames {
		ft.getTraceIndex(0, frame)
	}

	children := ft.nodes[0].Children
	for i := 1; i < len(children); i++ {
		if comparePythonFrameKey(children[i-1].Frame, children[i].Frame) > 0 {
			t.Fatalf("children not sorted at %d: %#v > %#v", i, children[i-1].Frame, children[i].Frame)
		}
	}
}

func encodeCodeObjectRecord(codeID uint64, funcName, filename string, firstLine int64, lineTable []byte) []byte {
	var buf bytes.Buffer
	buf.Write(encodeVarint(codeID))
	buf.WriteString(funcName)
	buf.WriteByte(0)
	buf.WriteString(filename)
	buf.WriteByte(0)
	buf.Write(encodeSignedVarint(firstLine))
	buf.Write(encodeVarint(uint64(len(lineTable))))
	buf.Write(lineTable)
	return buf.Bytes()
}

func encodeVarint(v uint64) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(buf, v)
	return buf[:n]
}

func encodeSignedVarint(v int64) []byte {
	uv := uint64(v<<1) ^ uint64(v>>63)
	return encodeVarint(uv)
}
