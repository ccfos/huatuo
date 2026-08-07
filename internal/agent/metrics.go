// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"bufio"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SummaryOptions controls how raw Prometheus text metrics are converted into a
// compact, read-only summary suitable for scripts and AI/automation agents.
type SummaryOptions struct {
	// Prefixes limits returned samples to metric names with one of the prefixes.
	// Empty Prefixes means all metrics are considered.
	Prefixes       []string
	TopN           int
	IncludeSamples bool
	Now            func() time.Time
}

// MetricSample is a single parsed Prometheus text exposition sample.
type MetricSample struct {
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels,omitempty"`
	Value    float64           `json:"value"`
	Category string            `json:"category"`
}

// Summary is a small machine-readable view over HUATUO metrics. It intentionally
// excludes any mutating operation; callers can use it to reason about system
// health without giving agents write access to the node or the kernel.
type Summary struct {
	Source         string         `json:"source"`
	GeneratedAt    string         `json:"generated_at"`
	TotalSamples   int            `json:"total_samples"`
	MatchedSamples int            `json:"matched_samples"`
	Categories     map[string]int `json:"categories"`
	Top            []MetricSample `json:"top"`
	Samples        []MetricSample `json:"samples,omitempty"`
}

// BuildSummary parses Prometheus text metrics and returns a compact summary.
func BuildSummary(source, text string, opts SummaryOptions) (*Summary, error) {
	if opts.TopN < 0 {
		return nil, fmt.Errorf("topN must be >= 0")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	samples, err := ParsePrometheusText(text)
	if err != nil {
		return nil, err
	}

	matched := make([]MetricSample, 0, len(samples))
	categories := make(map[string]int)
	for _, sample := range samples {
		if !matchesPrefix(sample.Name, opts.Prefixes) {
			continue
		}
		sample.Category = categorizeMetric(sample.Name)
		matched = append(matched, sample)
		categories[sample.Category]++
	}

	top := append([]MetricSample(nil), matched...)
	sort.SliceStable(top, func(i, j int) bool {
		return math.Abs(top[i].Value) > math.Abs(top[j].Value)
	})
	if opts.TopN > 0 && len(top) > opts.TopN {
		top = top[:opts.TopN]
	}

	summary := &Summary{
		Source:         source,
		GeneratedAt:    opts.Now().UTC().Format(time.RFC3339),
		TotalSamples:   len(samples),
		MatchedSamples: len(matched),
		Categories:     categories,
		Top:            top,
	}
	if opts.IncludeSamples {
		summary.Samples = matched
	}
	return summary, nil
}

// ParsePrometheusText parses the Prometheus text exposition format enough for
// HUATUO's /metrics output and common exporters. It skips HELP/TYPE comments and
// ignores unsupported histogram metadata lines that do not contain a sample.
func ParsePrometheusText(text string) ([]MetricSample, error) {
	var samples []MetricSample
	scanner := bufio.NewScanner(strings.NewReader(text))
	// Allow long metric lines with many labels.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sample, ok, err := parseSampleLine(line)
		if err != nil {
			return nil, fmt.Errorf("parse metrics line %d: %w", lineNo, err)
		}
		if ok {
			samples = append(samples, sample)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

func parseSampleLine(line string) (MetricSample, bool, error) {
	nameAndLabels, rest := splitFirstField(line)
	if nameAndLabels == "" || rest == "" {
		return MetricSample{}, false, nil
	}

	valueField, _ := splitFirstField(rest)
	value, err := strconv.ParseFloat(valueField, 64)
	if err != nil {
		return MetricSample{}, false, fmt.Errorf("invalid value %q: %w", valueField, err)
	}
	// Prometheus text format permits NaN and +/-Inf, but encoding/json cannot
	// marshal non-finite floating-point values. Ignore them so the CLI always
	// emits valid JSON for downstream automation.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return MetricSample{}, false, nil
	}

	name := nameAndLabels
	labels := map[string]string(nil)
	if i := strings.IndexByte(nameAndLabels, '{'); i >= 0 {
		if !strings.HasSuffix(nameAndLabels, "}") {
			return MetricSample{}, false, fmt.Errorf("invalid label set %q", nameAndLabels)
		}
		name = nameAndLabels[:i]
		labelText := strings.TrimSuffix(nameAndLabels[i+1:], "}")
		parsedLabels, err := parseLabels(labelText)
		if err != nil {
			return MetricSample{}, false, err
		}
		labels = parsedLabels
	}

	if name == "" {
		return MetricSample{}, false, fmt.Errorf("empty metric name")
	}
	return MetricSample{Name: name, Labels: labels, Value: value}, true, nil
}

func splitFirstField(s string) (string, string) {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r == ' ' || r == '\t' {
			return s[:i], strings.TrimSpace(s[i:])
		}
	}
	return s, ""
}

func parseLabels(labelText string) (map[string]string, error) {
	labels := make(map[string]string)
	if strings.TrimSpace(labelText) == "" {
		return labels, nil
	}

	for len(labelText) > 0 {
		labelText = strings.TrimSpace(labelText)
		eq := strings.IndexByte(labelText, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("invalid label near %q", labelText)
		}
		key := strings.TrimSpace(labelText[:eq])
		labelText = strings.TrimSpace(labelText[eq+1:])
		if !strings.HasPrefix(labelText, "\"") {
			return nil, fmt.Errorf("label %q must use quoted value", key)
		}

		value, consumed, err := parseQuoted(labelText)
		if err != nil {
			return nil, fmt.Errorf("label %q: %w", key, err)
		}
		labels[key] = value
		labelText = strings.TrimSpace(labelText[consumed:])
		if strings.HasPrefix(labelText, ",") {
			labelText = labelText[1:]
			continue
		}
		if labelText != "" {
			return nil, fmt.Errorf("unexpected label suffix %q", labelText)
		}
	}
	return labels, nil
}

func parseQuoted(s string) (string, int, error) {
	var b strings.Builder
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if escaped {
			switch c {
			case 'n':
				b.WriteByte('\n')
			case '\\', '"':
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			return b.String(), i + 1, nil
		}
		b.WriteByte(c)
	}
	return "", 0, fmt.Errorf("unterminated quoted value")
}

func matchesPrefix(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" || strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func categorizeMetric(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "cpu") || strings.Contains(n, "load"):
		return "cpu"
	case strings.Contains(n, "mem") || strings.Contains(n, "oom") || strings.Contains(n, "page"):
		return "memory"
	case strings.Contains(n, "io") || strings.Contains(n, "disk") || strings.Contains(n, "block"):
		return "io"
	case strings.Contains(n, "net") || strings.Contains(n, "tcp") || strings.Contains(n, "udp") || strings.Contains(n, "drop"):
		return "network"
	case strings.Contains(n, "sched") || strings.Contains(n, "runqueue") || strings.Contains(n, "softlockup") || strings.Contains(n, "hungtask"):
		return "scheduler"
	case strings.Contains(n, "trace") || strings.Contains(n, "profile") || strings.Contains(n, "flame") || strings.Contains(n, "perf"):
		return "profiling"
	case strings.Contains(n, "container") || strings.Contains(n, "cgroup") || strings.Contains(n, "pod"):
		return "container"
	case strings.Contains(n, "error") || strings.Contains(n, "fail") || strings.Contains(n, "timeout"):
		return "errors"
	default:
		return "other"
	}
}
