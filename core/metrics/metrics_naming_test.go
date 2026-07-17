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

package collector

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// metricCallInfo records a single NewGaugeData/NewCounterData call site.
type metricCallInfo struct {
	file     string
	line     int
	funcName string // NewGaugeData, NewCounterData, NewContainerGaugeData, NewContainerCounterData
	name     string // the metric name argument (if it's a string literal)
	isString bool   // true if the name argument was a string literal
}

// validMetricName matches Prometheus metric naming rules:
// [a-zA-Z_:][a-zA-Z0-9_:]*
var validMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// findMetricCalls walks all .go files in dir and collects calls to the
// four metric constructor functions.
func findMetricCalls(t *testing.T, dir string) []metricCallInfo {
	var calls []metricCallInfo

	targetFuncs := map[string]bool{
		"NewGaugeData":            true,
		"NewCounterData":          true,
		"NewContainerGaugeData":   true,
		"NewContainerCounterData": true,
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		node, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			t.Logf("Warning: failed to parse %s: %v", path, parseErr)
			return nil
		}

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			var funcName string
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				funcName = fn.Name
			case *ast.SelectorExpr:
				funcName = fn.Sel.Name
			default:
				return true
			}

			if !targetFuncs[funcName] {
				return true
			}

			info := metricCallInfo{
				file:     path,
				line:     fset.Position(call.Pos()).Line,
				funcName: funcName,
			}

			if len(call.Args) >= 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					info.name = strings.Trim(lit.Value, `"`)
					info.isString = true
				}
			}

			calls = append(calls, info)
			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("filepath.Walk failed: %v", err)
	}

	return calls
}

// TestMetricNamesValid checks that all metric names defined via
// NewGaugeData, NewCounterData, NewContainerGaugeData, and
// NewContainerCounterData in core/metrics/ are valid Prometheus metric names.
func TestMetricNamesValid(t *testing.T) {
	calls := findMetricCalls(t, ".")

	if len(calls) == 0 {
		t.Skip("No metric calls found — package directory may have changed")
	}

	var violations []string
	for _, c := range calls {
		if !c.isString {
			continue // skip dynamic names (can't validate statically)
		}

		if !validMetricName.MatchString(c.name) {
			relPath, _ := filepath.Rel(".", c.file)
			violations = append(violations, fmt.Sprintf(
				"%s:%d: invalid metric name %q (does not match [a-zA-Z_:][a-zA-Z0-9_:]*)",
				relPath, c.line, c.name,
			))
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("Invalid metric names found:\n%s", strings.Join(violations, "\n"))
	}
}

// TestCounterNamesHaveTotalSuffix checks that counter-type metrics
// (created via NewCounterData or NewContainerCounterData) with static
// string names end with "_total".
//
// This follows the Prometheus naming convention:
// https://prometheus.io/docs/practices/naming/#metric-names
//
// A allowlist is provided for existing metrics that don't follow the
// convention yet but are exposed to users and can't be renamed without
// breaking dashboards.
func TestCounterNamesHaveTotalSuffix(t *testing.T) {
	calls := findMetricCalls(t, ".")

	// Allowlist for counters that don't end with _total.
	// These are typically dynamic or kernel-derived names that
	// are exposed as-is for compatibility.
	allowlist := map[string]bool{
		// Ascend NPU HBM error counters use _cnt suffix (vendor convention)
		"npu_hbm_single_bit_error_cnt":          true,
		"npu_hbm_double_bit_error_cnt":          true,
		"npu_hbm_total_single_bit_error_cnt":    true,
		"npu_hbm_total_double_bit_error_cnt":    true,
		"npu_hbm_single_bit_isolated_pages_cnt": true,
		"npu_hbm_double_bit_isolated_pages_cnt": true,
		"npu_mac_tx_mac_pause_num":              true,
		"npu_mac_rx_mac_pause_num":              true,
	}

	var violations []string
	for _, c := range calls {
		if !c.isString {
			continue
		}

		isCounter := c.funcName == "NewCounterData" || c.funcName == "NewContainerCounterData"
		if !isCounter {
			continue
		}

		if allowlist[c.name] {
			continue
		}

		if !strings.HasSuffix(c.name, "_total") {
			relPath, _ := filepath.Rel(".", c.file)
			violations = append(violations, fmt.Sprintf(
				"%s:%d: counter metric %q should end with _total (func: %s)",
				relPath, c.line, c.name, c.funcName,
			))
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("Counter metrics without _total suffix:\n%s\n"+
			"If these are intentional, add them to the allowlist in this test.",
			strings.Join(violations, "\n"))
	}
}

// TestMetricNamesUseSnakeCase checks that metric names use snake_case
// (lowercase with underscores) and don't contain camelCase or hyphens.
func TestMetricNamesUseSnakeCase(t *testing.T) {
	calls := findMetricCalls(t, ".")

	// Allow uppercase for kernel-derived dynamic names that are
	// exposed as-is (e.g., Tcp_Inuse, TcpExt_ListenDrops).
	// We only flag obvious camelCase patterns in static names.
	camelCase := regexp.MustCompile(`[a-z][A-Z]`)

	var violations []string
	for _, c := range calls {
		if !c.isString {
			continue
		}

		if camelCase.MatchString(c.name) {
			relPath, _ := filepath.Rel(".", c.file)
			violations = append(violations, fmt.Sprintf(
				"%s:%d: metric name %q contains camelCase — use snake_case instead",
				relPath, c.line, c.name,
			))
		}

		if strings.Contains(c.name, "-") {
			relPath, _ := filepath.Rel(".", c.file)
			violations = append(violations, fmt.Sprintf(
				"%s:%d: metric name %q contains hyphen — use underscore instead",
				relPath, c.line, c.name,
			))
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("Metric names with non-snake_case characters:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestMetricNamesNotEmpty checks that metric names are not empty strings.
func TestMetricNamesNotEmpty(t *testing.T) {
	calls := findMetricCalls(t, ".")

	for _, c := range calls {
		if c.isString && c.name == "" {
			relPath, _ := filepath.Rel(".", c.file)
			t.Errorf("%s:%d: empty metric name in %s call",
				relPath, c.line, c.funcName)
		}
	}
}
