// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"huatuo-bamai/internal/agent"
)

const defaultMetricsURL = "http://127.0.0.1:19704/metrics"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "huatuo-insight: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("huatuo-insight", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	metricsURL := fs.String("metrics-url", defaultMetricsURL, "HUATUO /metrics endpoint")
	file := fs.String("file", "", "read Prometheus text metrics from file instead of HTTP")
	prefix := fs.String("prefix", "", "comma-separated metric name prefixes to include; empty means all")
	topN := fs.Int("top", 20, "number of largest absolute-value samples to include in top list")
	timeout := fs.Duration("timeout", 5*time.Second, "HTTP request timeout")
	pretty := fs.Bool("pretty", false, "pretty-print JSON output")
	includeSamples := fs.Bool("include-samples", false, "include all matched samples in addition to the top list")
	help := fs.Bool("help", false, "show help")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *help {
		fs.SetOutput(out)
		fs.Usage()
		return nil
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	source, text, err := readMetrics(*file, *metricsURL, *timeout)
	if err != nil {
		return err
	}

	summary, err := agent.BuildSummary(source, text, agent.SummaryOptions{
		Prefixes:       splitCSV(*prefix),
		TopN:           *topN,
		IncludeSamples: *includeSamples,
	})
	if err != nil {
		return err
	}

	var payload []byte
	if *pretty {
		payload, err = json.MarshalIndent(summary, "", "  ")
	} else {
		payload, err = json.Marshal(summary)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(payload))
	return err
}

func readMetrics(file, metricsURL string, timeout time.Duration) (string, string, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", "", err
		}
		return file, string(data), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, http.NoBody)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET %s: unexpected status %s", metricsURL, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	return metricsURL, string(data), nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
