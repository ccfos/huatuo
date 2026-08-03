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

package bpf

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

type kprobeAttachOptions struct {
	program     *loadedProgram
	symbol      string
	isRetprobe  bool
	linkOptions link.KprobeOptions
}

type tracepointAttachOptions struct {
	program     *loadedProgram
	system      string
	symbol      string
	linkOptions link.TracepointOptions
}

type rawTracepointAttachOptions struct {
	program     *loadedProgram
	linkOptions link.RawTracepointOptions
}

func parseSectionSymbol(sectionName string) (string, error) {
	parts := strings.SplitN(sectionName, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("invalid section name %q", sectionName)
	}

	return parts[1], nil
}

func parseKprobeAttachOptions(
	program *loadedProgram,
	symbol string,
	isRetprobe bool,
	retprobeMaxActive int,
) (kprobeAttachOptions, error) {
	if symbol == "" {
		return kprobeAttachOptions{}, errors.New("empty kprobe symbol")
	}
	if retprobeMaxActive < 0 {
		return kprobeAttachOptions{}, fmt.Errorf(
			"invalid retprobe max active %d",
			retprobeMaxActive,
		)
	}
	if !isRetprobe && retprobeMaxActive != 0 {
		return kprobeAttachOptions{}, errors.New(
			"retprobe max active requires a kretprobe",
		)
	}

	opts := kprobeAttachOptions{
		program:    program,
		symbol:     symbol,
		isRetprobe: isRetprobe,
		linkOptions: link.KprobeOptions{
			RetprobeMaxActive: retprobeMaxActive,
		},
	}
	if isRetprobe {
		if strings.Contains(symbol, "+") {
			return kprobeAttachOptions{}, fmt.Errorf("invalid kretprobe symbol %q", symbol)
		}
		return opts, nil
	}

	parts := strings.Split(symbol, "+")
	if len(parts) > 2 || parts[0] == "" {
		return kprobeAttachOptions{}, fmt.Errorf("invalid kprobe symbol %q", symbol)
	}
	opts.symbol = parts[0]
	if len(parts) == 1 {
		return opts, nil
	}

	offset, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return kprobeAttachOptions{}, fmt.Errorf("parse kprobe offset in %q: %w", symbol, err)
	}
	opts.linkOptions.Offset = offset
	return opts, nil
}

func parseTracepointAttachOptions(
	program *loadedProgram,
	symbol string,
) (tracepointAttachOptions, error) {
	parts := strings.SplitN(symbol, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return tracepointAttachOptions{}, fmt.Errorf("invalid tracepoint symbol %q", symbol)
	}

	return tracepointAttachOptions{
		program: program,
		system:  parts[0],
		symbol:  parts[1],
	}, nil
}

func parseRawTracepointAttachOptions(
	program *loadedProgram,
	symbol string,
) (rawTracepointAttachOptions, error) {
	if symbol == "" {
		return rawTracepointAttachOptions{}, errors.New("empty raw tracepoint symbol")
	}

	return rawTracepointAttachOptions{
		program: program,
		linkOptions: link.RawTracepointOptions{
			Name:    symbol,
			Program: program.handle,
		},
	}, nil
}

func parsePerfEventAttachOptions(
	program *loadedProgram,
	samplePeriod uint64,
	sampleFreq uint64,
	cpuIDs []int,
) (*perfEventOption, error) {
	if samplePeriod != 0 && sampleFreq != 0 {
		return nil, fmt.Errorf(
			"%w: sample period and frequency are mutually exclusive",
			errInvalidPerfEventOption,
		)
	}

	opt := &perfEventOption{
		sample:  sampleFreq,
		program: program.handle,
		cpuIDs:  cpuIDs,
	}
	if samplePeriod != 0 {
		opt.sample = samplePeriod
		opt.sampleMode = perfEventSamplePeriod
	}

	return opt, nil
}

// AttachWithOptions attaches programs with options.
func (b *defaultBPF) AttachWithOptions(opts []AttachOption) error {
	if err := b.acquireWriteLock(); err != nil {
		return err
	}
	defer b.mu.Unlock()

	return b.attachWithOptions(opts)
}

func (b *defaultBPF) attachWithOptions(opts []AttachOption) (err error) {
	defer func() {
		if err != nil { // detach all programs when error.
			if detachErr := b.detach(); detachErr != nil {
				log.WithError(detachErr).WithField("bpf", b.name).
					Warn("failed to detach BPF after attach failure")
			}
		}
	}()

	for _, opt := range opts {
		progID := b.ProgramIDByName(opt.ProgramName)
		program, ok := b.programsByID[progID]
		if !ok {
			return fmt.Errorf("unknown BPF program %q", opt.ProgramName)
		}

		switch program.programType {
		case ebpf.TracePoint:
			attachOpts, parseErr := parseTracepointAttachOptions(program, opt.Symbol)
			if parseErr != nil {
				return parseErr
			}
			if err = b.attachTracepoint(attachOpts); err != nil {
				return err
			}
		case ebpf.Kprobe:
			attachOpts, parseErr := parseKprobeAttachOptions(
				program,
				opt.Symbol,
				program.sectionPrefix == "kretprobe",
				opt.Kprobe.RetprobeMaxActive,
			)
			if parseErr != nil {
				return parseErr
			}
			if err = b.attachKprobe(attachOpts); err != nil {
				return err
			}
		case ebpf.RawTracepoint:
			attachOpts, parseErr := parseRawTracepointAttachOptions(program, opt.Symbol)
			if parseErr != nil {
				return parseErr
			}
			if err = b.attachRawTracepoint(attachOpts); err != nil {
				return err
			}
		case ebpf.PerfEvent:
			attachOpts, parseErr := parsePerfEventAttachOptions(
				program,
				opt.PerfEvent.SamplePeriod,
				opt.PerfEvent.SampleFreq,
				opt.PerfEvent.CPUIDs,
			)
			if parseErr != nil {
				return parseErr
			}
			if err = b.attachPerfEvent(attachOpts); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported BPF program type %q", program.programType)
		}
	}

	return nil
}

// Attach the default programs.
func (b *defaultBPF) Attach() error {
	if err := b.acquireWriteLock(); err != nil {
		return err
	}
	defer b.mu.Unlock()

	return b.attach()
}

func (b *defaultBPF) attach() (err error) {
	defer func() {
		if err != nil { // detach all programs when error.
			if detachErr := b.detach(); detachErr != nil {
				log.WithError(detachErr).WithField("bpf", b.name).
					Warn("failed to detach BPF after attach failure")
			}
		}
	}()

	for _, program := range b.programsByID {
		switch program.programType {
		case ebpf.TracePoint:
			symbol, parseErr := parseSectionSymbol(program.sectionName)
			if parseErr != nil {
				return parseErr
			}
			attachOpts, parseErr := parseTracepointAttachOptions(program, symbol)
			if parseErr != nil {
				return fmt.Errorf("parse BPF section %q: %w", program.sectionName, parseErr)
			}
			if err = b.attachTracepoint(attachOpts); err != nil {
				return err
			}
		case ebpf.Kprobe:
			symbol, parseErr := parseSectionSymbol(program.sectionName)
			if parseErr != nil {
				return parseErr
			}
			attachOpts, parseErr := parseKprobeAttachOptions(
				program,
				symbol,
				program.sectionPrefix == "kretprobe",
				0,
			)
			if parseErr != nil {
				return fmt.Errorf("parse BPF section %q: %w", program.sectionName, parseErr)
			}
			if err = b.attachKprobe(attachOpts); err != nil {
				return err
			}
		case ebpf.RawTracepoint:
			symbol, parseErr := parseSectionSymbol(program.sectionName)
			if parseErr != nil {
				return parseErr
			}
			attachOpts, parseErr := parseRawTracepointAttachOptions(program, symbol)
			if parseErr != nil {
				return fmt.Errorf("parse BPF section %q: %w", program.sectionName, parseErr)
			}
			if err = b.attachRawTracepoint(attachOpts); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported BPF program type %q", program.programType)
		}
	}

	return nil
}

func (b *defaultBPF) attachKprobe(opts kprobeAttachOptions) error {
	linkKey := opts.symbol
	attach := link.Kretprobe
	attachType := "kretprobe"
	if !opts.isRetprobe {
		linkKey = fmt.Sprintf("%s+%d", opts.symbol, opts.linkOptions.Offset)
		attach = link.Kprobe
		attachType = "kprobe"
	}
	if _, ok := opts.program.links[linkKey]; ok {
		return fmt.Errorf("%w: %s %q", ErrDuplicateAttach, attachType, linkKey)
	}

	l, err := attach(opts.symbol, opts.program.handle, &opts.linkOptions)
	if err != nil {
		return fmt.Errorf("attach %s %q: %w", attachType, opts.symbol, err)
	}

	opts.program.links[linkKey] = l
	log.WithField("attach_type", attachType).
		WithField("symbol", opts.symbol).
		WithField("link_count", len(opts.program.links)).
		Debug("attached BPF program")
	return nil
}

func (b *defaultBPF) attachTracepoint(opts tracepointAttachOptions) error {
	linkKey := fmt.Sprintf("%s/%s", opts.system, opts.symbol)
	if _, ok := opts.program.links[linkKey]; ok {
		return fmt.Errorf("%w: tracepoint %q", ErrDuplicateAttach, linkKey)
	}

	l, err := link.Tracepoint(
		opts.system,
		opts.symbol,
		opts.program.handle,
		&opts.linkOptions,
	)
	if err != nil {
		return fmt.Errorf("attach tracepoint %s/%s: %w", opts.system, opts.symbol, err)
	}

	opts.program.links[linkKey] = l
	log.WithField("attach_target", linkKey).
		WithField("link_count", len(opts.program.links)).
		Debug("attached BPF tracepoint")
	return nil
}

func (b *defaultBPF) attachRawTracepoint(opts rawTracepointAttachOptions) error {
	linkKey := opts.linkOptions.Name
	if _, ok := opts.program.links[linkKey]; ok {
		return fmt.Errorf("%w: raw tracepoint %q", ErrDuplicateAttach, linkKey)
	}

	l, err := link.AttachRawTracepoint(opts.linkOptions)
	if err != nil {
		return fmt.Errorf("attach raw tracepoint %q: %w", linkKey, err)
	}

	opts.program.links[linkKey] = l
	log.WithField("attach_target", linkKey).
		WithField("link_count", len(opts.program.links)).
		Debug("attached BPF raw tracepoint")
	return nil
}

func (b *defaultBPF) attachPerfEvent(opt *perfEventOption) error {
	if b.perfEvent != nil {
		return fmt.Errorf("%w: perf event", ErrDuplicateAttach)
	}

	if opt.sample == 0 {
		return types.ErrArgsInvalid
	}

	event, err := attachPerfEvent(opt)
	if err != nil {
		return fmt.Errorf("attach perf event: %w", err)
	}

	b.perfEvent = event
	log.WithField("cpu_ids", opt.cpuIDs).Debug("attached BPF perf event")
	return nil
}

// Detach all programs. Collects individual detach errors and returns a
// combined error so callers can detect cleanup failures.
func (b *defaultBPF) Detach() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.isClosed {
		return nil
	}

	return b.detach()
}

func (b *defaultBPF) detach() error {
	var detachErrs []error

	for _, program := range b.programsByID {
		for linkKey, l := range program.links {
			if l != nil {
				if err := l.Close(); err != nil {
					detachErrs = append(detachErrs, fmt.Errorf(
						"detach link %q from program %q: %w",
						linkKey,
						program.name,
						err,
					))
				}
			}
		}
		program.links = make(map[string]link.Link)
	}

	if b.perfEvent != nil {
		if err := b.perfEvent.detach(); err != nil {
			detachErrs = append(detachErrs, fmt.Errorf("detach perf event: %w", err))
		}
		b.perfEvent = nil
	}

	return errors.Join(detachErrs...)
}
