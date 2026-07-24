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

import "testing"

func TestParseLinetableEmpty(t *testing.T) {
	// An empty line table resolves to firstlineno regardless of version, and a
	// non-empty table still needs a non-negative instruction offset.
	lineno, ok := parseLinetable(pythonVersion311, nil, 4, 17)
	if !ok || lineno != 17 {
		t.Fatalf("expected firstlineno=17 ok=true, got %d ok=%v", lineno, ok)
	}
}

func TestParseLinetable39(t *testing.T) {
	// Legacy co_lnotab byte pairs: (start_delta, line_delta).
	// firstlineno=1; offsets 0->line2, 2->line2, 6->line3.
	table := []byte{0, 1, 2, 0, 4, 1}
	cases := []struct {
		instr int64
		want  int
	}{
		{1, 2},
		{3, 2},
		{6, 3},
	}
	for _, c := range cases {
		lineno, ok := parseLinetable(0x03090000, table, c.instr, 1)
		if !ok {
			t.Fatalf("instr=%d: expected ok", c.instr)
		}
		if lineno != c.want {
			t.Fatalf("instr=%d: expected lineno=%d got %d", c.instr, c.want, lineno)
		}
	}
}

func TestParseLinetable311(t *testing.T) {
	// Compact PEP 626 table. firstlineno=10.
	//   ONE_LINE1 (code=11, +1 line) covering word range [0,1) -> line 11
	//   ONE_LINE0 (code=10, +0 line) covering word range [1,2) -> line 11
	// Each ONE_LINE entry is firstByte + column + end_column.
	table := []byte{
		(11 << 3), 1, 2, // range [0,1) line 11
		(10 << 3), 3, 4, // range [1,2) line 11
	}
	// instrOffset is byte-based; the parser divides by 2 to get the word index.
	if lineno, ok := parseLinetable(pythonVersion311, table, 0, 10); !ok || lineno != 11 {
		t.Fatalf("instr=0: expected 11 ok=true, got %d ok=%v", lineno, ok)
	}
	if lineno, ok := parseLinetable(pythonVersion311, table, 2, 10); !ok || lineno != 11 {
		t.Fatalf("instr=2: expected 11 ok=true, got %d ok=%v", lineno, ok)
	}
	// Instruction past the end of the table cannot be resolved.
	if _, ok := parseLinetable(pythonVersion311, table, 4, 10); ok {
		t.Fatalf("instr=4: expected ok=false past end of table")
	}
}

func TestParseLinetable311ShortFormConsumesColumnByte(t *testing.T) {
	// A SHORT entry (code 0-9) must consume its extra column byte so subsequent
	// entries stay aligned. code=1, length=1 -> firstByte=(1<<3)|0=8, then one
	// extra byte. Followed by ONE_LINE1.
	table := []byte{
		(1 << 3), 7, // SHORT range [0,1) line unchanged (10)
		(11 << 3), 1, 2, // ONE_LINE1 range [1,2) line 11
	}
	if lineno, ok := parseLinetable(pythonVersion311, table, 0, 10); !ok || lineno != 10 {
		t.Fatalf("SHORT range instr=0: expected 10, got %d ok=%v", lineno, ok)
	}
	if lineno, ok := parseLinetable(pythonVersion311, table, 2, 10); !ok || lineno != 11 {
		t.Fatalf("ONE_LINE1 range instr=2: expected 11, got %d ok=%v", lineno, ok)
	}
}
