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

// This file ports memray's line-table decoding (src/memray/_memray/compat.cpp)
// so a captured instruction offset can be resolved to a source line. The three
// formats mirror CPython's evolution of the code object location table:
//   - Python 3.11+: the compact PEP 626 co_linetable (parseLinetable311)
//   - Python 3.10:   the word-code co_linetable (parseLinetable310)
//   - Python <3.10:  the legacy co_lnotab byte-pair table (parseLinetable39)

const (
	pythonVersion311 = 0x030B0000
	pythonVersion310 = 0x030A0000

	// noLineNumber marks "no line number" entries in the Python 3.10 line table.
	noLineNumber int8 = -128
)

// code-location info kinds for Python 3.11+ (see _PyCodeLocationInfoKind).
// Codes 0-9 (SHORT forms) are handled by the default branch of the switch.
const (
	locInfoOneLine0  = 10
	locInfoOneLine1  = 11
	locInfoOneLine2  = 12
	locInfoNoColumns = 13
	locInfoLong      = 14
	locInfoNone      = 15
)

// parseLinetable resolves instrOffset to a source line for the given line
// table. pythonVersion is PY_VERSION_HEX as captured in the stream header. It
// returns the line number and ok=true on success. An empty line table resolves
// to firstlineno.
func parseLinetable(pythonVersion int32, linetable []byte, instrOffset int64, firstlineno int) (int, bool) {
	if len(linetable) == 0 {
		return firstlineno, true
	}
	switch {
	case pythonVersion >= pythonVersion311:
		return parseLinetable311(instrOffset, linetable, firstlineno)
	case pythonVersion >= pythonVersion310:
		return parseLinetable310(instrOffset, linetable, firstlineno)
	default:
		return parseLinetable39(instrOffset, linetable, firstlineno)
	}
}

// parseLinetable311 decodes the compact PEP 626 location table used by
// CPython 3.11 and newer. It is a faithful port of memray's parseLinetable311.
func parseLinetable311(instrOffset int64, linetable []byte, firstlineno int) (int, bool) {
	// Instruction offsets are byte-based; the table is indexed in 2-byte
	// words, so convert to a word offset before matching.
	addrq := uint64(instrOffset) / 2
	lineno := firstlineno

	i := 0
	addr := uint64(0)

	// scanVarint reads a varint encoded in 6-bit chunks with bit 6 (0x40) as
	// the continuation flag, as used by the 3.11 location table.
	scanVarint := func() (uint32, bool) {
		if i >= len(linetable) {
			return 0, false
		}
		b := uint32(linetable[i])
		i++
		val := b & 63
		shift := uint(0)
		for b&64 != 0 {
			if i >= len(linetable) {
				return 0, false
			}
			b = uint32(linetable[i])
			i++
			shift += 6
			val |= (b & 63) << shift
		}
		return val, true
	}
	scanSignedVarint := func() (int, bool) {
		uval, ok := scanVarint()
		if !ok {
			return 0, false
		}
		sval := int(uval >> 1)
		if uval&1 != 0 {
			sval = -sval
		}
		return sval, true
	}
	readByte := func() (byte, bool) {
		if i >= len(linetable) {
			return 0, false
		}
		b := linetable[i]
		i++
		return b, true
	}

	for i < len(linetable) {
		firstByte, ok := readByte()
		if !ok {
			return lineno, false
		}
		code := (firstByte >> 3) & 15
		length := uint64(firstByte&7) + 1
		endAddr := addr + length

		switch code {
		case locInfoNone:
			// No location info for this range.
		case locInfoLong:
			lineDelta, ok := scanSignedVarint()
			if !ok {
				return lineno, false
			}
			lineno += lineDelta
			// end_lineno delta, column, end_column are present but unused for
			// folded output; they must still be consumed to stay aligned.
			if _, ok = scanVarint(); !ok {
				return lineno, false
			}
			if _, ok = scanVarint(); !ok {
				return lineno, false
			}
			if _, ok = scanVarint(); !ok {
				return lineno, false
			}
		case locInfoNoColumns:
			lineDelta, ok := scanSignedVarint()
			if !ok {
				return lineno, false
			}
			lineno += lineDelta
		case locInfoOneLine0, locInfoOneLine1, locInfoOneLine2:
			lineno += int(code) - locInfoOneLine0
			if _, ok = readByte(); !ok { // column
				return lineno, false
			}
			if _, ok = readByte(); !ok { // end_column
				return lineno, false
			}
		default:
			// SHORT forms (codes 0-9) carry a single extra byte encoding the
			// column delta; the line is unchanged. Consume it to stay aligned.
			if _, ok = readByte(); !ok {
				return lineno, false
			}
		}

		if addr <= addrq && endAddr > addrq {
			return lineno, true
		}
		addr = endAddr
	}
	return lineno, false
}

// parseLinetable310 decodes the word-code co_linetable used by CPython 3.10.
func parseLinetable310(instrOffset int64, linetable []byte, firstlineno int) (int, bool) {
	codeLineno := firstlineno
	// The table indexes word-code (2 bytes per instruction).
	lastExecuted := uint64(instrOffset) << 1
	var current uint64
	for i := 0; i+1 < len(linetable); i += 2 {
		startDelta := uint64(linetable[i])
		lineDelta := int8(linetable[i+1])
		current += startDelta
		if lineDelta != noLineNumber {
			codeLineno += int(lineDelta)
		}
		if current > lastExecuted {
			break
		}
	}
	return codeLineno, true
}

// parseLinetable39 decodes the legacy co_lnotab byte-pair table used by
// CPython 3.9 and earlier.
func parseLinetable39(instrOffset int64, linetable []byte, firstlineno int) (int, bool) {
	codeLineno := firstlineno
	var bc uint64
	i := 0
	for {
		if i >= len(linetable) {
			break
		}
		bc += uint64(linetable[i])
		i++
		if bc > uint64(instrOffset) {
			break
		}
		if i >= len(linetable) {
			break
		}
		codeLineno += int(int8(linetable[i]))
		i++
	}
	return codeLineno, true
}
