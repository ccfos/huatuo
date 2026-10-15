//go:build linux

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

// resource-sampler observes one live process, not a feature's exclusive cost.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/procfs"
)

const (
	formatVersion  = 2
	sampleInterval = 100 * time.Millisecond
	maxDuration    = 30 * time.Second
	maxSampleGap   = 2 * sampleInterval
)

type options struct {
	pid       int
	outputDir string
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stdout, "usage: resource-sampler --pid PID --output-dir ABSOLUTE_DIRECTORY")
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("resource-sampler", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.IntVar(&opts.pid, "pid", 0, "target process PID")
	flags.StringVar(&opts.outputDir, "output-dir", "", "new capture directory")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, errors.New("unexpected positional arguments")
	}
	if opts.pid <= 0 {
		return opts, errors.New("require a positive --pid")
	}
	if !filepath.IsAbs(opts.outputDir) {
		return opts, errors.New("require an absolute --output-dir")
	}
	return opts, nil
}

func run(opts options) error {
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGUSR1)
	defer signal.Stop(stopCh)
	return capture(ctx, opts, stopCh)
}

func capture(ctx context.Context, opts options, stopCh <-chan os.Signal) error {
	directory, err := os.Open(opts.outputDir)
	if err != nil {
		return fmt.Errorf("open capture directory: %w", err)
	}
	// Keep publishing into the original directory if the caller moves its path.
	defer directory.Close()
	opts.outputDir = fmt.Sprintf("/proc/self/fd/%d", directory.Fd())
	var summary aggregate
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		return finish(opts, &summary, err)
	}
	proc, err := fs.Proc(opts.pid)
	if err != nil {
		return finish(opts, &summary, err)
	}
	first, err := readSample(ctx, proc)
	if err != nil {
		return finish(opts, &summary, err)
	}
	if err := summary.add(first); err != nil {
		return finish(opts, &summary, err)
	}
	ctx, cancel := context.WithDeadline(ctx, first.cpuAt.Add(maxDuration))
	defer cancel()
	ready := struct {
		FormatVersion int `json:"format_version"`
		SamplerPID    int `json:"sampler_pid"`
		TargetPID     int `json:"target_pid"`
	}{FormatVersion: formatVersion, SamplerPID: os.Getpid(), TargetPID: opts.pid}
	if err := writeJSON(filepath.Join(opts.outputDir, "ready.json"), ready); err != nil {
		return finish(opts, &summary, err)
	}

	nextAt := first.cpuAt.Add(sampleInterval)
	timer := time.NewTimer(time.Until(nextAt))
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return finish(opts, &summary, err)
		}
		// A queued stop takes precedence over a simultaneously ready timer.
		select {
		case <-stopCh:
			return finalSample(ctx, opts, proc, &summary)
		default:
		}
		timer.Reset(time.Until(nextAt))
		select {
		case <-ctx.Done():
			return finish(opts, &summary, ctx.Err())
		case <-stopCh:
			return finalSample(ctx, opts, proc, &summary)
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			return finish(opts, &summary, err)
		}
		select {
		case <-stopCh:
			return finalSample(ctx, opts, proc, &summary)
		default:
		}
		summary.maxSamplingDelay = max(summary.maxSamplingDelay, time.Since(nextAt))
		sample, err := readSample(ctx, proc)
		if err != nil {
			return finish(opts, &summary, err)
		}
		if err := summary.add(sample); err != nil {
			return finish(opts, &summary, err)
		}
		summary.periodicSamples++
		nextAt = nextAt.Add(sampleInterval)
		if now := time.Now(); !nextAt.After(now) {
			skipped := int(now.Sub(nextAt)/sampleInterval) + 1
			summary.skippedSamples += skipped
			nextAt = nextAt.Add(time.Duration(skipped) * sampleInterval)
		}
	}
}

func finalSample(ctx context.Context, opts options, proc procfs.Proc, summary *aggregate) error {
	sample, err := readSample(ctx, proc)
	if err == nil {
		err = summary.add(sample)
	}
	if err == nil {
		err = ctx.Err()
	}
	return finish(opts, summary, err)
}

type sample struct {
	pid        int
	startTicks uint64
	cpuAt      time.Time
	ioAt       time.Time
	readSpan   time.Duration
	cpuSeconds float64
	rssBytes   int64
	fds        int64
	io         procfs.ProcIO
}

func readSample(ctx context.Context, proc procfs.Proc) (*sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	stat, err := proc.Stat()
	if err != nil {
		return nil, fmt.Errorf("read process stat: %w", err)
	}
	s := &sample{
		pid: proc.PID, startTicks: stat.Starttime, cpuAt: time.Now(),
		cpuSeconds: stat.CPUTime(), rssBytes: int64(stat.ResidentMemory()),
	}
	if strings.ContainsAny(stat.State, "ZXx") {
		return nil, errors.New("target process exited")
	}
	if s.rssBytes < 0 {
		return nil, errors.New("invalid process rss")
	}
	fds, err := proc.FileDescriptorsLen()
	if err != nil {
		return nil, fmt.Errorf("read process file descriptors: %w", err)
	}
	s.fds = int64(fds)
	s.io, err = proc.IO()
	if err != nil {
		return nil, fmt.Errorf("read process io: %w", err)
	}
	s.ioAt = time.Now()
	recheck, err := proc.Stat()
	if err != nil {
		return nil, fmt.Errorf("recheck process stat: %w", err)
	}
	if recheck.Starttime != s.startTicks || strings.ContainsAny(recheck.State, "ZXx") {
		return nil, errors.New("target process changed or exited during sample")
	}
	s.readSpan = time.Since(startedAt)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

type gauge struct {
	Before int64 `json:"before"`
	After  int64 `json:"after"`
	Delta  int64 `json:"delta"`
	Peak   int64 `json:"peak"`
}

func (g *gauge) update(value int64) {
	g.After = value
	g.Delta = value - g.Before
	g.Peak = max(g.Peak, value)
}

type aggregate struct {
	first            *sample
	last             *sample
	peakBase         *sample
	samples          int
	periodicSamples  int
	cpuPeakWindows   int
	cpuPeak          float64
	cpuPeakWindow    time.Duration
	rss              gauge
	fds              gauge
	skippedSamples   int
	maxSamplingDelay time.Duration
	maxSampleGap     time.Duration
	maxSampleSpan    time.Duration
}

func (a *aggregate) add(s *sample) error {
	if a.first == nil {
		a.first, a.last, a.peakBase = s, s, s
		a.rss = gauge{Before: s.rssBytes, After: s.rssBytes, Peak: s.rssBytes}
		a.fds = gauge{Before: s.fds, After: s.fds, Peak: s.fds}
		a.samples = 1
		a.maxSampleSpan = s.readSpan
		return nil
	}
	previous := a.last
	if s.pid != previous.pid || s.startTicks != previous.startTicks {
		return errors.New("target process identity changed")
	}
	cpuGap, ioGap := s.cpuAt.Sub(previous.cpuAt), s.ioAt.Sub(previous.ioAt)
	if cpuGap <= 0 || ioGap <= 0 {
		return errors.New("non-positive sample interval")
	}
	if s.cpuSeconds < previous.cpuSeconds {
		return errors.New("cpu counter decreased")
	}
	oldIO := [4]uint64{previous.io.ReadBytes, previous.io.WriteBytes, previous.io.RChar, previous.io.WChar}
	newIO := [4]uint64{s.io.ReadBytes, s.io.WriteBytes, s.io.RChar, s.io.WChar}
	for i, value := range newIO {
		if value < oldIO[i] {
			return errors.New("io counter decreased")
		}
	}
	// Short intervals accumulate; a final 1 ms tick must not become a 1000% peak.
	if window := s.cpuAt.Sub(a.peakBase.cpuAt); window >= sampleInterval {
		percent := (s.cpuSeconds - a.peakBase.cpuSeconds) / window.Seconds() * 100
		if a.cpuPeakWindows == 0 || percent > a.cpuPeak {
			a.cpuPeak, a.cpuPeakWindow = percent, window
		}
		a.cpuPeakWindows++
		a.peakBase = s
	}
	a.rss.update(s.rssBytes)
	a.fds.update(s.fds)
	a.maxSampleSpan = max(a.maxSampleSpan, s.readSpan)
	a.maxSampleGap = max(a.maxSampleGap, cpuGap, ioGap)
	a.last = s
	a.samples++
	return nil
}

type counter struct {
	Delta     uint64  `json:"delta"`
	PerSecond float64 `json:"per_second"`
}

func counterDiff(before, after uint64, seconds float64) counter {
	return counter{Delta: after - before, PerSecond: float64(after-before) / seconds}
}

type cpuUsage struct {
	Mean float64  `json:"mean"`
	Peak *float64 `json:"peak"`
}

type ioUsage struct {
	ReadBytes  counter `json:"read_bytes"`
	WriteBytes counter `json:"write_bytes"`
	RChar      counter `json:"rchar"`
	WChar      counter `json:"wchar"`
}

type quality struct {
	UsableForPeak bool     `json:"usable_for_peak"`
	Reasons       []string `json:"reasons"`
}

type captureResult struct {
	FormatVersion            int       `json:"format_version"`
	Complete                 bool      `json:"complete"`
	Errors                   []string  `json:"errors"`
	PID                      int       `json:"pid"`
	ProcessStartTimeTicks    *uint64   `json:"process_start_time_ticks"`
	IOIncludesWaitedChildren bool      `json:"io_includes_waited_children"`
	SampleIntervalMS         int64     `json:"sample_interval_ms"`
	SampleCount              int       `json:"sample_count"`
	PeriodicSampleCount      int       `json:"periodic_sample_count"`
	CPUWindowSeconds         *float64  `json:"cpu_window_seconds"`
	IOWindowSeconds          *float64  `json:"io_window_seconds"`
	CPUSeconds               *float64  `json:"cpu_seconds"`
	CPUPercent               *cpuUsage `json:"cpu_percent"`
	CPUPeakWindowCount       int       `json:"cpu_peak_window_count"`
	MinCPUPeakWindowMS       int64     `json:"min_cpu_peak_window_ms"`
	CPUPeakWindowSeconds     *float64  `json:"cpu_peak_window_seconds"`
	CPUPeakTailMS            *float64  `json:"cpu_peak_tail_ms"`
	RSSBytes                 *gauge    `json:"rss_bytes"`
	FDs                      *gauge    `json:"fds"`
	IO                       *ioUsage  `json:"io"`
	SkippedSamples           int       `json:"skipped_samples"`
	MaxSampleGapMS           float64   `json:"max_sample_gap_ms"`
	MaxSamplingDelayMS       float64   `json:"max_sampling_delay_ms"`
	MaxSampleSpanMS          float64   `json:"max_sample_span_ms"`
	Quality                  quality   `json:"quality"`
}

func (a *aggregate) result(pid int, cause error) captureResult {
	r := captureResult{
		FormatVersion: formatVersion, Complete: cause == nil, Errors: []string{}, PID: pid,
		IOIncludesWaitedChildren: true, SampleIntervalMS: sampleInterval.Milliseconds(),
		SampleCount: a.samples, PeriodicSampleCount: a.periodicSamples,
		CPUPeakWindowCount: a.cpuPeakWindows, MinCPUPeakWindowMS: sampleInterval.Milliseconds(),
		SkippedSamples: a.skippedSamples, MaxSampleGapMS: a.maxSampleGap.Seconds() * 1000,
		MaxSamplingDelayMS: a.maxSamplingDelay.Seconds() * 1000, MaxSampleSpanMS: a.maxSampleSpan.Seconds() * 1000,
		Quality: quality{Reasons: []string{}},
	}
	if cause != nil {
		r.Errors = append(r.Errors, cause.Error())
		r.Quality.Reasons = append(r.Quality.Reasons, "capture_incomplete")
	}
	if a.periodicSamples == 0 || a.cpuPeakWindows == 0 {
		r.Quality.Reasons = append(r.Quality.Reasons, "insufficient_samples")
	}
	if a.skippedSamples != 0 {
		r.Quality.Reasons = append(r.Quality.Reasons, "skipped_samples")
	}
	if a.maxSampleGap > maxSampleGap || a.maxSampleSpan > sampleInterval {
		r.Quality.Reasons = append(r.Quality.Reasons, "sample_gap_or_span_exceeded")
	}
	r.Quality.UsableForPeak = len(r.Quality.Reasons) == 0
	if a.first == nil {
		return r
	}
	r.ProcessStartTimeTicks, r.RSSBytes, r.FDs = &a.first.startTicks, &a.rss, &a.fds
	if a.samples < 2 {
		return r
	}
	cpuWindow := a.last.cpuAt.Sub(a.first.cpuAt).Seconds()
	ioWindow := a.last.ioAt.Sub(a.first.ioAt).Seconds()
	cpuSeconds := a.last.cpuSeconds - a.first.cpuSeconds
	tailMS := a.last.cpuAt.Sub(a.peakBase.cpuAt).Seconds() * 1000
	r.CPUWindowSeconds, r.IOWindowSeconds, r.CPUSeconds, r.CPUPeakTailMS = &cpuWindow, &ioWindow, &cpuSeconds, &tailMS
	r.CPUPercent = &cpuUsage{Mean: cpuSeconds / cpuWindow * 100}
	if a.cpuPeakWindows > 0 {
		window := a.cpuPeakWindow.Seconds()
		r.CPUPercent.Peak, r.CPUPeakWindowSeconds = &a.cpuPeak, &window
	}
	r.IO = &ioUsage{
		ReadBytes:  counterDiff(a.first.io.ReadBytes, a.last.io.ReadBytes, ioWindow),
		WriteBytes: counterDiff(a.first.io.WriteBytes, a.last.io.WriteBytes, ioWindow),
		RChar:      counterDiff(a.first.io.RChar, a.last.io.RChar, ioWindow),
		WChar:      counterDiff(a.first.io.WChar, a.last.io.WChar, ioWindow),
	}
	return r
}

func finish(opts options, summary *aggregate, cause error) error {
	if cause == nil && summary.samples < 2 {
		cause = errors.New("capture ended without two valid samples")
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		cause = fmt.Errorf("capture timed out: %w", cause)
	}
	if errors.Is(cause, context.Canceled) {
		cause = fmt.Errorf("capture aborted: %w", cause)
	}
	result := summary.result(opts.pid, cause)
	if err := writeJSON(filepath.Join(opts.outputDir, "result.json"), &result); err != nil {
		return errors.Join(cause, err)
	}
	if cause != nil {
		return cause
	}
	if !result.Quality.UsableForPeak {
		return fmt.Errorf("sampling quality unusable: %s", strings.Join(result.Quality.Reasons, ", "))
	}
	return nil
}

func writeJSON(path string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".resource-*.json")
	if err != nil {
		return fmt.Errorf("create result file: %w", err)
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(value)
	if err := errors.Join(encodeErr, file.Close()); err != nil {
		return fmt.Errorf("write result file: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("publish result file: %w", err)
	}
	return nil
}
