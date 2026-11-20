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

package tui

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type functionWeights struct {
	self  int64
	total int64
}

// TopTable builds the hotspot rows from functions across all stack paths.
type TopTable map[string]functionWeights

// Add attributes a positive weight to each frame and self weight to the leaf.
// Repeated frames contribute to Total at each occurrence, as in Grafana.
func (t TopTable) Add(ctx context.Context, frames []string, weight int64) error {
	for i, name := range frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := t[name]
		if weight > math.MaxInt64-row.total {
			return fmt.Errorf("total weight for function %q exceeds int64", name)
		}
		row.total += weight
		if i == len(frames)-1 {
			row.self += weight
		}
		t[name] = row
	}
	return nil
}

type topRow struct {
	name    string
	weights functionWeights
}

type topTableModel struct {
	rows        []topRow
	cursor      int
	unit        string
	sortByTotal bool
}

func (m *Model) setTopTable(ctx context.Context, weights TopTable, unit string) error {
	rows := make([]topRow, 0, len(weights))
	for name, weight := range weights {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows = append(rows, topRow{
			name:    name,
			weights: weight,
		})
	}
	if len(rows) != 0 {
		m.topTable = &topTableModel{
			rows: rows,
			unit: unit,
		}
		m.topTable.sortRows()
	}
	return ctx.Err()
}

func (top *topTableModel) sortRows() {
	slices.SortFunc(top.rows, func(a, b topRow) int {
		order := cmp.Compare(b.weights.self, a.weights.self)
		if top.sortByTotal {
			order = cmp.Compare(b.weights.total, a.weights.total)
		}
		if order == 0 {
			return cmp.Compare(a.name, b.name)
		}
		return order
	})
	top.cursor = 0
}

func (m *Model) topTableHeight() int {
	return max(m.height-headerLines, 0)
}

func (m *Model) renderTopTable() string {
	const heading = "HUATUO tui flamegraph\nkeys: v view  s sort  up/down select row  r root  q quit\n"
	top := m.topTable
	order := "Self"
	if top.sortByTotal {
		order = "Total"
	}
	status := "view: Top Table | sort: " + order
	if top.unit != "" {
		status += " | unit: " + terminalText(top.unit)
	}
	var out strings.Builder
	out.WriteString(heading + status + "\n")

	page := m.topTableHeight()
	start := max(0, top.cursor-page+1)
	rows := top.rows[start:min(len(top.rows), start+page)]
	var maxSelf, maxTotal int64
	for _, row := range rows {
		maxSelf = max(maxSelf, row.weights.self)
		maxTotal = max(maxTotal, row.weights.total)
	}
	// Reserve two columns for the selection marker and two for separators.
	width := max(m.safeWidth()-4, 0)
	fullSelfWidth := max(len("Self"), len(strconv.FormatInt(maxSelf, 10)))
	selfWidth := min(fullSelfWidth, width/2)
	totalWidth := min(max(len("Total"), len(strconv.FormatInt(maxTotal, 10))), width-selfWidth)
	selfWidth = min(fullSelfWidth, width-totalWidth)
	nameWidth := width - selfWidth - totalWidth
	writeRow := func(marker, name, self, total string) {
		fmt.Fprintf(&out, "%s%s %*s %*s\n", marker, padLabel(name, nameWidth),
			selfWidth, ansi.Truncate(self, selfWidth, "…"),
			totalWidth, ansi.Truncate(total, totalWidth, "…"))
	}
	writeRow("  ", "Function", "Self", "Total")
	for i, row := range rows {
		marker := "  "
		if start+i == top.cursor {
			marker = "> "
		}
		writeRow(marker, row.name, strconv.FormatInt(row.weights.self, 10), strconv.FormatInt(row.weights.total, 10))
	}
	return out.String()
}
