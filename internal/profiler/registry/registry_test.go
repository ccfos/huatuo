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

package registry

import (
	"context"
	"strings"
	"testing"

	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
)

// fakeProfiler satisfies Profiler with no behavior. Registry tests exercise
// the lookup tables, not sampling, so a stub is enough.
type fakeProfiler struct{}

func (fakeProfiler) Start(*pcontext.ProfilerContext) error         { return nil }
func (fakeProfiler) ReadDataLoop(context.Context, func(any)) error { return nil }
func (fakeProfiler) Stop(*pcontext.ProfilerContext) error          { return nil }

// resetRegistry isolates the package-level map per test so order doesn't matter
// and nothing leaks across tests.
func resetRegistry(t *testing.T) {
	t.Helper()

	saved := profilerRegistry
	profilerRegistry = map[string]map[string]ProfilerMeta{}

	t.Cleanup(func() { profilerRegistry = saved })
}

func newMeta(lang, typ string) ProfilerMeta {
	return ProfilerMeta{
		Type:        typ,
		LangOrImpl:  lang,
		Description: "fake",
		Impl:        fakeProfiler{},
		Aggregator:  func(*pcontext.ProfilerContext) *aggregator.Aggregator { return nil },
	}
}

func TestRegisterThenGet(t *testing.T) {
	resetRegistry(t)
	Register(newMeta("native", "cpu"))

	got, err := Get("native", "cpu")
	if err != nil {
		t.Fatalf(`Get("native", "cpu") error = %v, want nil`, err)
	}

	if got.LangOrImpl != "native" || got.Type != "cpu" {
		t.Errorf(`Get("native", "cpu") = (%q, %q), want ("native", "cpu")`,
			got.LangOrImpl, got.Type)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetRegistry(t)
	Register(newMeta("native", "cpu"))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal(`Register duplicate ("native", "cpu") did not panic`)
		}

		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "duplicate") {
			t.Errorf(`Register panic = %v, want message containing "duplicate"`, r)
		}
	}()

	Register(newMeta("native", "cpu"))
}

func TestGetFallbackToDefaultImpl(t *testing.T) {
	resetRegistry(t)
	Register(newMeta("native", "cpu"))

	// "go" is not registered directly; defaultLangProfiler["go"] = "native"
	// must redirect the lookup to ("native", "cpu").
	got, err := Get("go", "cpu")
	if err != nil {
		t.Fatalf(`Get("go", "cpu") error = %v, want nil`, err)
	}

	if got.LangOrImpl != "native" {
		t.Errorf(`Get("go", "cpu").LangOrImpl = %q, want "native"`, got.LangOrImpl)
	}
}

func TestGetExactBeatsFallback(t *testing.T) {
	resetRegistry(t)
	// Both registered: ("go", "cpu") must win over the ("native", "cpu")
	// fallback, otherwise a per-language override would be unreachable.
	Register(newMeta("native", "cpu"))
	Register(newMeta("go", "cpu"))

	got, err := Get("go", "cpu")
	if err != nil {
		t.Fatalf(`Get("go", "cpu") error = %v, want nil`, err)
	}

	if got.LangOrImpl != "go" {
		t.Errorf(`Get("go", "cpu").LangOrImpl = %q, want "go"`, got.LangOrImpl)
	}
}

func TestGetFallbackMissesWhenTypeAbsent(t *testing.T) {
	resetRegistry(t)
	Register(newMeta("native", "cpu"))

	// Lang fallback resolves "go" -> "native", but ("native", "mem") is not
	// registered, so the second lookup also misses.
	if _, err := Get("go", "mem"); err == nil {
		t.Fatal(`Get("go", "mem") error = nil, want non-nil`)
	}
}

func TestGetUnknownLang(t *testing.T) {
	resetRegistry(t)

	if _, err := Get("rust", "cpu"); err == nil {
		t.Fatal(`Get("rust", "cpu") error = nil, want non-nil`)
	}
}
