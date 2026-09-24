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

//go:build !didi

package bpf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ccfos/huatuo/internal/log"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/features"
)

// tracingMode names the entry point selected for a hook that ships both a
// kprobe and an fentry program.
type tracingMode string

const (
	tracingModeFentry tracingMode = "fentry"
	tracingModeKprobe tracingMode = "kprobe"
)

// TracingVariantPair declares the two entry points of one hook. Both programs
// must hook target. The fentry program is preferred because it needs no
// int3/breakpoint patching; the kprobe program is the compatibility fallback
// for kernels without fentry support.
//
// Pairing is explicit on purpose. Inferring kprobe/fentry twins from program
// names would silently select the wrong program for objects that were never
// migrated.
type TracingVariantPair struct {
	// Kprobe is the program name of the kprobe entry point.
	Kprobe string
	// Fentry is the program name of the fentry entry point.
	Fentry string
	// Target is the kernel function both entry points hook.
	Target string
}

// validate reports whether the pair carries the names it needs.
func (p TracingVariantPair) validate() error {
	switch {
	case p.Kprobe == "":
		return errors.New("bpf: tracing variant pair has no kprobe program")
	case p.Fentry == "":
		return errors.New("bpf: tracing variant pair has no fentry program")
	case p.Target == "":
		return errors.New("bpf: tracing variant pair has no target")
	default:
		return nil
	}
}

// targets returns the kernel functions the pairs hook, for diagnostics.
func tracingPairTargets(pairs []TracingVariantPair) []string {
	targets := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		targets = append(targets, pair.Target)
	}
	return targets
}

// errTracingTargetUnsupported reports that the running kernel is known to be
// unable to attach an fentry program to a target.
var errTracingTargetUnsupported = errors.New("bpf: fentry target unsupported")

// IsTracingTargetUnsupported reports whether err says the kernel cannot support
// the entry point at all: the target is missing from the kernel BTF, or the
// kernel does not offer the tracing program type this build needs.
//
// It is false for permission, resource and verifier failures, so a caller does
// not mistake a temporary or environmental failure for a missing capability.
func IsTracingTargetUnsupported(err error) bool {
	return errors.Is(err, errTracingTargetUnsupported) || errors.Is(err, ebpf.ErrNotSupported)
}

// tracingProbe reports whether the kernel can attach an fentry program to
// target.
//
// A nil error means the target looks usable. An error matching
// errTracingTargetUnsupported means the kernel cannot support that target and
// the caller must not attempt a load. Any other error is a diagnostic only:
// only a real load attempt is authoritative, so transient probe failures
// (permissions, resources) must not be remembered as permanent
// incompatibility.
type tracingProbe func(target string) (err error)

// tracingAttempt loads one pruned collection spec, creates the event pipe
// reader and attaches every program that survived pruning. It must release
// everything it created before returning an error.
type tracingAttempt func(ctx context.Context, spec *ebpf.CollectionSpec) (BPF, PerfEventReader, error)

// tracingSelection records which entry point an attempt loaded, and why it was
// chosen over the other one.
type tracingSelection struct {
	Mode tracingMode
	// Reason explains why kprobe was selected without an fentry attempt.
	Reason string
}

// tracingCleanupError marks an attempt that failed and could not release all
// of its resources. Retrying could leave both entry points attached at once,
// so the coordinator stops instead of falling back.
type tracingCleanupError struct {
	err error
}

func (e *tracingCleanupError) Error() string {
	return fmt.Sprintf("bpf: attempt cleanup failed: %v", e.err)
}

func (e *tracingCleanupError) Unwrap() error {
	return e.err
}

// markTracingCleanupFailure marks err as a failed attempt whose resources were
// not all released.
func markTracingCleanupFailure(err error) error {
	if err == nil {
		return nil
	}

	return &tracingCleanupError{err: err}
}

// isTracingCleanupFailure reports whether err came from an attempt whose
// cleanup failed.
func isTracingCleanupFailure(err error) bool {
	var cleanupErr *tracingCleanupError

	return errors.As(err, &cleanupErr)
}

// releaseAfterFailure joins attemptErr with the errors observed while closing
// the attempt's resources. When a close fails the result is marked so the
// coordinator refuses to retry over possibly still-attached programs.
func releaseAfterFailure(attemptErr error, closers ...io.Closer) error {
	var closeErrs []error

	for _, closer := range closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil {
			closeErrs = append(closeErrs, err)
		}
	}

	cleanupErr := errors.Join(closeErrs...)
	if cleanupErr == nil {
		return attemptErr
	}

	return markTracingCleanupFailure(errors.Join(attemptErr, cleanupErr))
}

// probeFentryTarget reports whether the kernel can attach an fentry program to
// target.
//
// The kernel BTF lookup is the authoritative check: fentry programs are only
// loadable for functions present in the kernel BTF, so a target the BTF does
// not carry can never be attached. The program type probe is a diagnostic
// instead, for the reason tracingProbe documents: it drives the shared bpf_init
// target and maps any EINVAL it sees to "unsupported", which says nothing about
// target. Its failure keeps the real load of target as the judge.
func probeFentryTarget(target string) error {
	if target == "" {
		return fmt.Errorf("%w: empty target", errTracingTargetUnsupported)
	}

	if err := features.HaveProgramType(ebpf.Tracing); err != nil {
		return fmt.Errorf("probe tracing program type: %w", err)
	}

	kernelSpec, err := btf.LoadKernelSpec()
	if err != nil {
		return fmt.Errorf("%w: load kernel BTF: %w", errTracingTargetUnsupported, err)
	}

	var fn *btf.Func

	err = kernelSpec.TypeByName(target, &fn)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, btf.ErrNotFound):
		return fmt.Errorf("%w: kernel BTF has no function %q", errTracingTargetUnsupported, target)
	case errors.Is(err, btf.ErrMultipleMatches):
		return fmt.Errorf("%w: kernel BTF has multiple functions named %q", errTracingTargetUnsupported, target)
	default:
		return fmt.Errorf("look up %q in kernel BTF: %w", target, err)
	}
}

// validateTracingVariantPair checks that spec carries both entry points of the
// pair and that they hook the declared target.
func validateTracingVariantPair(spec *ebpf.CollectionSpec, pair TracingVariantPair) error {
	if err := pair.validate(); err != nil {
		return err
	}

	for name, want := range map[string]tracingMode{
		pair.Kprobe: tracingModeKprobe,
		pair.Fentry: tracingModeFentry,
	} {
		program, ok := spec.Programs[name]
		if !ok {
			return fmt.Errorf("bpf: tracing variant program %q not found in object", name)
		}

		symbol, err := parseSectionSymbol(program.SectionName)
		if err != nil {
			return fmt.Errorf("parse BPF section %q: %w", program.SectionName, err)
		}
		if symbol != pair.Target {
			return fmt.Errorf(
				"%s program %q hooks %q, want %q",
				want, name, symbol, pair.Target,
			)
		}
	}

	return nil
}

// tracingVariantKeep names the entry points of pairs that mode loads.
func tracingVariantKeep(pairs []TracingVariantPair, mode tracingMode) map[string]bool {
	keep := make(map[string]bool, len(pairs))

	for _, pair := range pairs {
		if mode == tracingModeKprobe {
			keep[pair.Kprobe] = true
			continue
		}

		keep[pair.Fentry] = true
	}

	return keep
}

// pruneTracingVariants removes every paired entry point that keep does not
// name. Every other program (tracepoints, unrelated hooks) is kept untouched so
// the object keeps its ABI and its event stream.
//
// spec must be a private copy: pruning happens before the collection is
// loaded, because a single unsupported program makes the whole load fail.
func pruneTracingVariants(spec *ebpf.CollectionSpec, pairs []TracingVariantPair, keep map[string]bool) error {
	for _, pair := range pairs {
		if err := validateTracingVariantPair(spec, pair); err != nil {
			return err
		}

		for _, name := range []string{pair.Kprobe, pair.Fentry} {
			if !keep[name] {
				delete(spec.Programs, name)
			}
		}
	}

	return nil
}

// selectTracingVariant decides which entry point the first attempt uses.
//
// Default is fentry. A target the kernel is known not to support selects
// kprobe without a doomed load attempt. Any other probe failure is kept as a
// diagnostic and still tries fentry, because only the load is authoritative.
func selectTracingVariant(pairs []TracingVariantPair, probe tracingProbe) (tracingMode, string) {
	var diagnostics []string

	for _, pair := range pairs {
		err := probe(pair.Target)
		switch {
		case err == nil:
		case errors.Is(err, errTracingTargetUnsupported):
			return tracingModeKprobe, err.Error()
		default:
			diagnostics = append(diagnostics, err.Error())
		}
	}

	if len(diagnostics) > 0 {
		log.WithField("hooks", tracingPairTargets(pairs)).
			WithField("diagnostics", strings.Join(diagnostics, "; ")).
			Debug("fentry probe failed, attempting fentry anyway")
	}

	return tracingModeFentry, ""
}

// loadTracingVariantWithFallback loads pristine and attaches the entry points
// selected for pairs, preferring fentry and falling back to kprobe once.
//
// pristine is never modified: every attempt loads its own copy, so constants
// are rewritten from the same baseline and the caller's spec stays usable.
// Only one attempt is live at a time, and a failed attempt releases its reader,
// links and handles before the next one starts, so the two entry points are
// never attached together.
func loadTracingVariantWithFallback(
	ctx context.Context,
	pristine *ebpf.CollectionSpec,
	pairs []TracingVariantPair,
	probe tracingProbe,
	attempt tracingAttempt,
) (BPF, PerfEventReader, tracingSelection, error) {
	if pristine == nil {
		return nil, nil, tracingSelection{}, errors.New("bpf: nil collection spec")
	}
	if len(pairs) == 0 {
		return nil, nil, tracingSelection{}, errors.New("bpf: no tracing variant pair")
	}
	if attempt == nil {
		return nil, nil, tracingSelection{}, errors.New("bpf: no tracing attempt")
	}

	mode, reason := selectTracingVariant(pairs, probe)
	selection := tracingSelection{Mode: mode, Reason: reason}

	object, reader, err := loadTracingVariantAttempt(ctx, pristine, pairs, selection, attempt)
	if err == nil {
		return object, reader, selection, nil
	}
	if mode != tracingModeFentry || isTracingCleanupFailure(err) {
		return nil, nil, tracingSelection{}, err
	}

	log.WithError(err).
		WithField("hooks", tracingPairTargets(pairs)).
		Warn("fentry entry point unavailable, falling back to kprobe")

	fallback := tracingSelection{Mode: tracingModeKprobe, Reason: err.Error()}

	object, reader, fallbackErr := loadTracingVariantAttempt(ctx, pristine, pairs, fallback, attempt)
	if fallbackErr != nil {
		// The kprobe error is the actionable one, the fentry error explains
		// why the fallback happened; report both instead of masking either.
		return nil, nil, tracingSelection{}, errors.Join(err, fallbackErr)
	}

	log.WithField("hooks", tracingPairTargets(pairs)).
		WithField("mode", string(fallback.Mode)).
		WithField("reason", fallback.Reason).
		Info("attached kprobe entry point after fentry fallback")

	return object, reader, fallback, nil
}

// LoadAttachAndEventPipeWithFallback loads bpfName from the default object
// directory, selects one entry point per explicitly paired hook, creates the
// event pipe for mapName and attaches the collection.
//
// It returns an object that is already attached and a reader that is already
// reading, so the caller must neither attach nor create the event pipe again.
// Every attempt loads its own copy of the object's spec, so an unsupported
// entry point is never loaded, and the fentry attempt is retried once with the
// kprobe entry point when it fails.
func LoadAttachAndEventPipeWithFallback(
	ctx context.Context,
	bpfName string,
	consts map[string]any,
	pairs []TracingVariantPair,
	mapName string,
	perCPUBufSize uint32,
) (BPF, PerfEventReader, error) {
	if err := validateName(bpfName); err != nil {
		return nil, nil, err
	}
	if mapName == "" {
		return nil, nil, errors.New("bpf: empty event map name")
	}

	pristine, err := loadCollectionSpec(bpfName)
	if err != nil {
		return nil, nil, err
	}

	attempt := func(ctx context.Context, spec *ebpf.CollectionSpec) (BPF, PerfEventReader, error) {
		return loadAttachAndEventPipe(ctx, bpfName, spec, consts, mapName, perCPUBufSize)
	}

	object, reader, selection, err := loadTracingVariantWithFallback(
		ctx, pristine, pairs, probeFentryTarget, attempt,
	)
	if err != nil {
		return nil, nil, err
	}

	// The selected entry point is what an operator has to check when a tracer
	// behaves differently on different kernels, so it is reported at load.
	log.WithField("bpf", bpfName).
		WithField("mode", string(selection.Mode)).
		WithField("hooks", tracingPairTargets(pairs)).
		Info("loaded BPF with a selected tracing entry point")

	return object, reader, nil
}

// loadAttachAndEventPipe loads one pruned spec copy, creates the event pipe
// reader and attaches the collection.
//
// The reader is created before the attach, exactly like AttachAndEventPipe, so
// events emitted while attaching are buffered instead of lost. Everything
// created here is released before an error is returned; a failing release is
// marked so the caller does not retry over handles that may still be attached.
func loadAttachAndEventPipe(
	ctx context.Context,
	bpfName string,
	spec *ebpf.CollectionSpec,
	consts map[string]any,
	mapName string,
	perCPUBufSize uint32,
) (BPF, PerfEventReader, error) {
	object, err := loadBPFFromCollectionSpec(bpfName, spec, consts)
	if err != nil {
		return nil, nil, err
	}

	inner, ok := object.(*defaultBPF)
	if !ok {
		return nil, nil, releaseAfterFailure(
			fmt.Errorf("loader returned %T, want *defaultBPF", object),
			object,
		)
	}

	m, err := inner.mapByName(mapName)
	if err != nil {
		return nil, nil, releaseAfterFailure(err, object)
	}

	reader, err := newPerfEventReader(ctx, m, int(perCPUBufSize))
	if err != nil {
		return nil, nil, releaseAfterFailure(err, object)
	}

	if err := object.Attach(); err != nil {
		return nil, nil, releaseAfterFailure(err, reader, object)
	}

	return object, reader, nil
}

// loadTracingVariantAttempt runs a single attempt against a private copy of
// pristine, pruned to the entry points selection picks.
func loadTracingVariantAttempt(
	ctx context.Context,
	pristine *ebpf.CollectionSpec,
	pairs []TracingVariantPair,
	selection tracingSelection,
	attempt tracingAttempt,
) (BPF, PerfEventReader, error) {
	spec := pristine.Copy()

	if err := pruneTracingVariants(spec, pairs, tracingVariantKeep(pairs, selection.Mode)); err != nil {
		return nil, nil, err
	}

	return attempt(ctx, spec)
}

// LoadAttachAndEventPipeForEntryPoint loads bpfName from the default object
// directory with only the paired entry point named by entryPoint, creates the
// event pipe for mapName and attaches the collection.
//
// It never falls back to the other entry point of a pair, so the error it
// returns is the reason that entry point could not be used:
// IsTracingTargetUnsupported separates a kernel that cannot support it from a
// permission, resource or verifier failure. Callers that want a tracer which
// keeps running on every kernel want LoadAttachAndEventPipeWithFallback; this
// one answers what the running kernel actually supports.
func LoadAttachAndEventPipeForEntryPoint(
	ctx context.Context,
	bpfName string,
	consts map[string]any,
	pairs []TracingVariantPair,
	entryPoint string,
	mapName string,
	perCPUBufSize uint32,
) (BPF, PerfEventReader, error) {
	if err := validateName(bpfName); err != nil {
		return nil, nil, err
	}
	if mapName == "" {
		return nil, nil, errors.New("bpf: empty event map name")
	}
	if !isPairedEntryPoint(pairs, entryPoint) {
		return nil, nil, fmt.Errorf("bpf: program %q is not an entry point of any pair", entryPoint)
	}

	pristine, err := loadCollectionSpec(bpfName)
	if err != nil {
		return nil, nil, err
	}

	spec := pristine.Copy()
	if err := pruneTracingVariants(spec, pairs, map[string]bool{entryPoint: true}); err != nil {
		return nil, nil, err
	}

	return loadAttachAndEventPipe(ctx, bpfName, spec, consts, mapName, perCPUBufSize)
}

// isPairedEntryPoint reports whether entryPoint is one of the two entry points
// of a pair. An unknown name would prune the whole pair and load an object
// whose hook is silently missing.
func isPairedEntryPoint(pairs []TracingVariantPair, entryPoint string) bool {
	for _, pair := range pairs {
		if entryPoint == pair.Kprobe || entryPoint == pair.Fentry {
			return true
		}
	}

	return false
}
