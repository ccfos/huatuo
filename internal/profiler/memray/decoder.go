// Copyright 2025 The HuaTuo Authors
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
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"huatuo-bamai/internal/symbol"
)

// Decoder implements a minimal memray-lite ALL_ALLOCATIONS stream reader.
// It reconstructs Python stacks (function only) and retained allocations.
// Native/hybrid stacks are supported at function granularity; line/file info remains TODO.

// Options controls decoding.
type Options struct {
	MergeThreads bool   // merge all threads into a single bucket
	Separator    string // separator for collapsed stacks; default ";"
	Metric       string // "bytes" (default) or "count"
	StackMode    StackMode
}

// StackMode selects how stacks are rendered in folded output.
type StackMode uint8

const (
	StackModePython StackMode = iota
	StackModeNative
	StackModeHybrid
)

// Header is the memray stream header.
type Header struct {
	Pid                int32
	HasNativeTraces    bool
	ShouldTracePyAlloc bool
	FileFormat         uint8
	CommandLine        string
	MainTid            uint64
	SkipFrames         uint64
	PythonVersion      int32
}

// reader holds decoding state.
type reader struct {
	r   io.Reader
	opt Options

	header Header

	// per-stream state
	lastDataPtr     uint64
	lastNativeFrame uint64
	lastInstrPtr    uint64
	lastThreadID    uint64
	recentPtrs      [15]uint64
	threadStacks    map[uint64][]uint64
	frameTree       frameTree
	codeObjects     map[uint64]codeObject

	nativeFrames    []nativeFrame
	nativeSymbolize *nativeSymbolizer

	simpleAllocs map[uint64]liveAlloc
	rangeAllocs  intervalTree
	tmpBuf       [1]byte
}

type codeObject struct {
	Func string
	// TODO(python-mem): Extend this struct to retain filename/firstlineno/linetable
	// and render line-aware stack labels. We currently keep function-only labels.
}

type locationKey struct {
	PythonFrameID uint64
	NativeFrameID uint64
	ThreadID      uint64
}

type liveAlloc struct {
	Size        uint64
	FrameIdx    uint64
	NativeFrame uint64
	ThreadID    uint64
}

// allocator kinds
type allocatorKindType int

const (
	allocatorKindSimpleAlloc allocatorKindType = iota + 1
	allocatorKindSimpleDealloc
	allocatorKindRangedAlloc
	allocatorKindRangedDealloc
)

// record types (token encoding matches record_writer.cpp)
const (
	recordTypeTrailer          = 1
	recordTypeMemoryRecord     = 2
	recordTypeNativeTraceIndex = 5
	recordTypeMemoryMapStart   = 6
	recordTypeSegmentHeader    = 7
	recordTypeSegment          = 8
	recordTypeThreadRecord     = 10
	recordTypeContextSwitch    = 12
	recordTypeCodeObject       = 14

	recordTypeFramePop   = 16 // 16..31
	recordTypeObject     = 32 // 32..63
	recordTypeFramePush  = 64 // 64..127
	recordTypeAllocation = 128
)

const (
	fileFormatAllAllocations = 0
)

func (rd *reader) readHeader() error {
	var magic [7]byte
	if _, err := io.ReadFull(rd.r, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != "memray\x00" {
		return fmt.Errorf("invalid magic %q", string(magic[:]))
	}
	var version int32
	if err := binary.Read(rd.r, binary.LittleEndian, &version); err != nil {
		return err
	}
	if version != 12 {
		return fmt.Errorf("unsupported header version %d", version)
	}
	var pyver int32
	var native uint8
	var fileFmt uint8
	var stats struct {
		NAlloc  uint64
		NFrames uint64
		StartMS int64
		EndMS   int64
	}

	if err := binary.Read(rd.r, binary.LittleEndian, &pyver); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &native); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &fileFmt); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &stats); err != nil {
		return err
	}
	cmd, err := rd.readCString()
	if err != nil {
		return err
	}

	var pid int32
	var mainTid uint64
	var skipped uint64
	var pyAlloc uint8
	var tracePy uint8
	var trackObj uint8
	if err := binary.Read(rd.r, binary.LittleEndian, &pid); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &mainTid); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &skipped); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &pyAlloc); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &tracePy); err != nil {
		return err
	}
	if err := binary.Read(rd.r, binary.LittleEndian, &trackObj); err != nil {
		return err
	}

	rd.header = Header{
		Pid:                pid,
		HasNativeTraces:    native != 0,
		ShouldTracePyAlloc: tracePy != 0,
		FileFormat:         fileFmt,
		CommandLine:        cmd,
		MainTid:            mainTid,
		SkipFrames:         skipped,
		PythonVersion:      pyver,
	}
	rd.threadStacks = make(map[uint64][]uint64)
	rd.codeObjects = make(map[uint64]codeObject)
	rd.simpleAllocs = make(map[uint64]liveAlloc)
	rd.nativeFrames = make([]nativeFrame, 0, 1024)
	if rd.header.HasNativeTraces {
		rd.nativeSymbolize = newNativeSymbolizer(rd.header.Pid)
	}
	return nil
}

func (rd *reader) handleContextSwitch() error {
	tid, err := rd.readUint64()
	if err != nil {
		return err
	}
	rd.lastThreadID = tid
	if _, ok := rd.threadStacks[tid]; !ok {
		rd.threadStacks[tid] = make([]uint64, 0, 32)
	}
	return nil
}

func (rd *reader) handleFramePush(flags uint8) error {
	var frame frameInfo
	frame.IsEntry = flags&1 == 1
	var err error
	if frame.CodeObjectID, err = rd.readVarint(); err != nil {
		return err
	}
	sv, err := rd.readSignedVarint()
	if err != nil {
		return err
	}
	frame.InstrOffset = sv

	frameID, err := packPythonFrameID(frame.CodeObjectID, frame.IsEntry)
	if err != nil {
		return err
	}
	stack := rd.threadStacks[rd.lastThreadID]
	parent := uint64(0)
	if len(stack) > 0 {
		parent = stack[len(stack)-1]
	}
	newIdx := rd.frameTree.getTraceIndex(parent, frameID)
	rd.threadStacks[rd.lastThreadID] = append(stack, newIdx)
	return nil
}

func (rd *reader) handleFramePop(flags uint8) error {
	count := int(flags) + 1
	stack := rd.threadStacks[rd.lastThreadID]
	if count > len(stack) {
		rd.threadStacks[rd.lastThreadID] = nil
		return nil
	}
	rd.threadStacks[rd.lastThreadID] = stack[:len(stack)-count]
	return nil
}

func (rd *reader) handleCodeObject() error {
	codeID, err := rd.readVarint()
	if err != nil {
		return err
	}
	funcName, err := rd.readCString()
	if err != nil {
		return err
	}
	if _, err := rd.readCString(); err != nil { // filename
		return err
	}
	if _, err := rd.readSignedVarint(); err != nil { // firstlineno delta
		return err
	}
	// TODO(python-mem): Decode and retain linetable payload so we can reconstruct
	// precise source line info in stack rendering.
	ltSize, err := rd.readVarint()
	if err != nil {
		return err
	}
	if ltSize > 0 {
		if _, err := io.CopyN(io.Discard, rd.r, int64(ltSize)); err != nil {
			return err
		}
	}
	// TODO(python-mem): Keep richer code object metadata and include it in rendered
	// stack frame labels. Today we intentionally map codeID -> function name only.
	rd.codeObjects[codeID] = codeObject{Func: funcName}
	return nil
}

func (rd *reader) threadKey(tid uint64) uint64 {
	if rd.opt.MergeThreads {
		return 0
	}
	return tid
}

func (rd *reader) skipSegmentHeader() error {
	if _, err := rd.readCString(); err != nil { // filename
		return err
	}
	numSeg, err := rd.readVarint()
	if err != nil {
		return err
	}
	if _, err := rd.readUintptr(); err != nil { // addr
		return err
	}
	for i := uint64(0); i < numSeg; i++ {
		var token [1]byte
		if _, err := io.ReadFull(rd.r, token[:]); err != nil {
			return err
		}
		if token[0] != recordTypeSegment {
			return fmt.Errorf("expected SEGMENT token, got %d", token[0])
		}
		if _, err := rd.readUintptr(); err != nil {
			return err
		}
		if _, err := rd.readVarint(); err != nil {
			return err
		}
	}
	return nil
}

func (rd *reader) skipMemoryRecord() error {
	if _, err := rd.readVarint(); err != nil {
		return err
	}
	if _, err := rd.readVarint(); err != nil {
		return err
	}
	return nil
}

func (rd *reader) handleNativeFrameIndex() error {
	deltaIP, err := rd.readSignedVarint()
	if err != nil {
		return err
	}
	rd.lastInstrPtr = uint64(int64(rd.lastInstrPtr) + deltaIP)
	deltaIdx, err := rd.readSignedVarint()
	if err != nil {
		return err
	}
	rd.lastNativeFrame = uint64(int64(rd.lastNativeFrame) + deltaIdx)
	rd.nativeFrames = append(rd.nativeFrames, nativeFrame{
		IP:   rd.lastInstrPtr,
		Next: rd.lastNativeFrame,
	})
	return nil
}

func (rd *reader) skipObjectRecord(flags uint8) error {
	ptrIdx := (flags >> 1) & 0x0f
	if ptrIdx == 0x0f {
		delta, err := rd.readSignedVarint()
		if err != nil {
			return err
		}
		rd.lastDataPtr = uint64(int64(rd.lastDataPtr) + delta)
		copy(rd.recentPtrs[1:], rd.recentPtrs[:len(rd.recentPtrs)-1])
		rd.recentPtrs[0] = rd.lastDataPtr << 3
	}
	if rd.header.HasNativeTraces && (flags&1) == 1 {
		if _, err := rd.readSignedVarint(); err != nil {
			return err
		}
	}
	return nil
}

func (rd *reader) extractTypeAndFlags(b byte) (recordType, flags uint8) {
	switch {
	case b&recordTypeAllocation != 0:
		return recordTypeAllocation, b & (recordTypeAllocation - 1)
	case b&recordTypeFramePush != 0:
		return recordTypeFramePush, b & (recordTypeFramePush - 1)
	case b&recordTypeObject != 0:
		return recordTypeObject, b & (recordTypeObject - 1)
	case b&recordTypeFramePop != 0:
		return recordTypeFramePop, b & (recordTypeFramePop - 1)
	default:
		return b, 0
	}
}

func (rd *reader) readCString() (string, error) {
	var buf []byte
	tmp := rd.tmpBuf[:]
	for {
		if _, err := io.ReadFull(rd.r, tmp); err != nil {
			return "", err
		}
		if tmp[0] == 0 {
			return string(buf), nil
		}
		buf = append(buf, tmp[0])
	}
}

func (rd *reader) readUint64() (uint64, error) {
	var v uint64
	err := binary.Read(rd.r, binary.LittleEndian, &v)
	return v, err
}

func (rd *reader) readUintptr() (uint64, error) {
	// Assume 64-bit
	return rd.readUint64()
}

func (rd *reader) readVarint() (uint64, error) {
	var res uint64
	var shift uint
	tmp := rd.tmpBuf[:]
	for {
		if _, err := io.ReadFull(rd.r, tmp); err != nil {
			return 0, err
		}
		b := tmp[0]
		res |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return res, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, fmt.Errorf("varint overflow")
		}
	}
}

func (rd *reader) readSignedVarint() (int64, error) {
	uv, err := rd.readVarint()
	if err != nil {
		return 0, err
	}
	val := int64((uv >> 1) ^ uint64((int64(uv&1)<<63)>>63))
	return val, nil
}

// frameTree mirrors memray FrameTree for Python frames.
type frameTree struct {
	// index 0 is root
	nodes      []frameNode
	badParents uint64
}

type frameNode struct {
	FrameID  uint64
	Parent   uint64
	Children []childEdge
}

type childEdge struct {
	FrameID  uint64
	ChildIdx uint64
}

type frameInfo struct {
	CodeObjectID uint64
	IsEntry      bool
	InstrOffset  int64
}

const maxPackedCodeObjectID = ^uint64(0) >> 1

func packPythonFrameID(codeID uint64, isEntry bool) (uint64, error) {
	// frameID packs codeID and isEntry into one uint64:
	// - bits [63:1]: codeID
	// - bit [0]: isEntry
	if codeID > maxPackedCodeObjectID {
		return 0, fmt.Errorf("code object id %d exceeds max packed range %d", codeID, maxPackedCodeObjectID)
	}
	frameID := codeID << 1
	if isEntry {
		frameID |= 1
	}
	return frameID, nil
}

func unpackPythonFrameID(frameID uint64) (codeID uint64, isEntry bool) {
	return frameID >> 1, frameID&1 == 1
}

func (ft *frameTree) getTraceIndex(parent, frameID uint64) uint64 {
	if ft.nodes == nil {
		ft.nodes = []frameNode{{}}
	}
	if parent >= uint64(len(ft.nodes)) {
		// Malformed stream; fall back to root to avoid panic.
		ft.badParents++
		parent = 0
	}
	edges := ft.nodes[parent].Children
	i := 0
	// TODO: use binary search? (Children are kept sorted by FrameID; linear scan is OK for now.)
	for i < len(edges) && edges[i].FrameID < frameID {
		i++
	}
	if i < len(edges) && edges[i].FrameID == frameID {
		return edges[i].ChildIdx
	}
	newIdx := uint64(len(ft.nodes))
	edges = append(edges, childEdge{})
	copy(edges[i+1:], edges[i:])
	edges[i] = childEdge{FrameID: frameID, ChildIdx: newIdx}
	ft.nodes[parent].Children = edges
	ft.nodes = append(ft.nodes, frameNode{FrameID: frameID, Parent: parent})
	return newIdx
}

// stackForLocation renders the collapsed stack for a location key.
func (rd *reader) stackForLocation(loc locationKey) string {
	switch rd.opt.StackMode {
	case StackModeNative:
		if loc.NativeFrameID != 0 {
			if stack := rd.nativeStackKey(loc.NativeFrameID); stack != "" {
				return stack
			}
		}
		return rd.pythonStackKey(loc.PythonFrameID, loc.ThreadID)
	case StackModeHybrid:
		if loc.NativeFrameID != 0 {
			if stack := rd.hybridStackKey(loc.ThreadID, loc.PythonFrameID, loc.NativeFrameID); stack != "" {
				return stack
			}
		}
		return rd.pythonStackKey(loc.PythonFrameID, loc.ThreadID)
	default:
		return rd.pythonStackKey(loc.PythonFrameID, loc.ThreadID)
	}
}

func (rd *reader) pythonStackFrames(idx uint64) ([]string, []bool) {
	if idx == 0 || int(idx) >= len(rd.frameTree.nodes) {
		return nil, nil
	}
	frames := make([]string, 0, 16)
	entries := make([]bool, 0, 16)
	for idx != 0 {
		node := rd.frameTree.nodes[idx]
		frameID := node.FrameID
		codeID, isEntry := unpackPythonFrameID(frameID)
		// TODO(python-mem): Switch to line/file aware labels once code object metadata
		// retention is implemented in handleCodeObject.
		fn := rd.codeObjects[codeID].Func
		if fn == "" {
			fn = "[unknown]"
		}
		frames = append(frames, fn)
		entries = append(entries, isEntry)
		idx = node.Parent
	}
	return frames, entries
}

func (rd *reader) pythonStackKey(idx, tid uint64) string {
	frames, _ := rd.pythonStackFrames(idx)
	if len(frames) == 0 {
		return ""
	}
	if tid == rd.header.MainTid && rd.header.SkipFrames > 0 {
		skip := int(rd.header.SkipFrames)
		if skip >= len(frames) {
			return ""
		}
		frames = frames[:len(frames)-skip]
	}
	return collapseFrames(frames, rd.opt.Separator)
}

func (rd *reader) nativeStackFrames(nativeID uint64) []string {
	if nativeID == 0 {
		return nil
	}
	frames := make([]string, 0, 32)
	for current := nativeID; current != 0; {
		idx := int(current - 1)
		if idx < 0 || idx >= len(rd.nativeFrames) {
			break
		}
		frame := rd.nativeFrames[idx]
		frames = append(frames, rd.symbolizeNative(frame.IP))
		current = frame.Next
	}
	return frames
}

func (rd *reader) nativeStackKey(nativeID uint64) string {
	frames := rd.nativeStackFrames(nativeID)
	if len(frames) == 0 {
		return ""
	}
	return collapseFrames(frames, rd.opt.Separator)
}

func (rd *reader) hybridStackKey(tid, pyID, nativeID uint64) string {
	frames := rd.hybridStackFrames(tid, pyID, nativeID)
	if len(frames) == 0 {
		return ""
	}
	return collapseFrames(frames, rd.opt.Separator)
}

func (rd *reader) hybridStackFrames(tid, pyID, nativeID uint64) []string {
	pythonStack, isEntry := rd.pythonStackFrames(pyID)
	if len(pythonStack) == 0 {
		return nil
	}
	nativeStack := rd.nativeStackFrames(nativeID)
	if len(nativeStack) == 0 {
		return pythonStack
	}

	numNonEntry := 0
	for _, entry := range isEntry {
		if !entry {
			numNonEntry++
		}
	}

	hybrid := make([]string, len(nativeStack)+numNonEntry)
	// Reverse native stack so we can pair from least recent to most recent.
	reverseStrings(nativeStack)

	pidx := len(pythonStack) - 1
	hidx := len(hybrid) - 1

	toSkip := 0
	if tid == rd.header.MainTid {
		toSkip = int(rd.header.SkipFrames)
	}
	firstKept := pidx - toSkip

	for _, nativeFrame := range nativeStack {
		if pidx >= 0 && isPythonBoundary(nativeFrame) {
			for {
				if toSkip != 0 && pidx == firstKept {
					hybrid = hybrid[:hidx+1]
				}
				if hidx < 0 {
					break
				}
				hybrid[hidx] = pythonStack[pidx]
				hidx--
				pidx--
				if pidx < 0 || isEntry[pidx] {
					break
				}
			}
		} else {
			if hidx < 0 {
				break
			}
			hybrid[hidx] = nativeFrame
			hidx--
		}
	}

	if pidx >= 0 {
		// Not enough native frames to pair; fall back to Python stack.
		return pythonStack
	}
	if hidx >= 0 {
		hybrid = hybrid[hidx+1:]
	}
	return hybrid
}

type nativeFrame struct {
	IP   uint64
	Next uint64
}

type nativeSymbolizer struct {
	pid   int32
	cache map[uint64]string
	usym  *symbol.UsymResolver
}

func newNativeSymbolizer(pid int32) *nativeSymbolizer {
	s := &nativeSymbolizer{
		pid:   pid,
		cache: make(map[uint64]string),
	}
	if pid > 0 {
		s.usym = symbol.NewUsymResolver()
	}
	return s
}

func (rd *reader) symbolizeNative(ip uint64) string {
	if rd.nativeSymbolize == nil || rd.nativeSymbolize.usym == nil {
		return fmt.Sprintf("0x%x", ip)
	}
	if name, ok := rd.nativeSymbolize.cache[ip]; ok {
		return name
	}
	name := rd.nativeSymbolize.usym.UsymStackStrsReversed(uint32(rd.nativeSymbolize.pid), []uint64{ip}, 1)
	if name[0] == "" {
		name[0] = fmt.Sprintf("0x%x", ip)
	}
	rd.nativeSymbolize.cache[ip] = name[0]
	return name[0]
}

func collapseFrames(frames []string, sep string) string {
	if len(frames) == 0 {
		return ""
	}
	reverseStrings(frames)
	return strings.Join(frames, sep)
}

func reverseStrings(vals []string) {
	for i, j := 0, len(vals)-1; i < j; i, j = i+1, j-1 {
		vals[i], vals[j] = vals[j], vals[i]
	}
}

func isPythonBoundary(symbol string) bool {
	if strings.Contains(symbol, "_PyEval_EvalFrameDefault") {
		return true
	}
	return strings.HasPrefix(symbol, "_TAIL_CALL_") && strings.Contains(symbol, ".llvm.")
}

// allocatorKind maps allocator ID to simplified kind.
func allocatorKind(id uint8) allocatorKindType {
	switch id {
	case 1, 5:
		return allocatorKindSimpleDealloc
	case 14:
		return allocatorKindRangedAlloc
	case 15:
		return allocatorKindRangedDealloc
	default:
		return allocatorKindSimpleAlloc
	}
}

// intervalTree tracks mmap-style ranged allocations.
type intervalTree struct {
	intervals []interval
}

type interval struct {
	start uint64
	end   uint64
	alloc liveAlloc
}

func (t *intervalTree) add(start, size uint64, alloc liveAlloc) {
	if size == 0 {
		return
	}
	t.intervals = append(t.intervals, interval{
		start: start,
		end:   start + size,
		alloc: alloc,
	})
}
