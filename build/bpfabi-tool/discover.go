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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cilium/ebpf/btf"
)

const anchorPrefix = "__bpf_abi_"

type domain struct {
	name       string
	prefix     string
	headerPath string
	outputPath string
}

type candidate struct {
	cName      string
	typ        btf.Type
	objectPath string
}

func discoverDomains(root string) ([]domain, error) {
	pattern := filepath.Join(root, "bpf", "include", "abi", "*_types.h")
	headers, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("scan abi headers: %w", err)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("no abi headers match %q", pattern)
	}

	sort.Strings(headers)
	domains := make([]domain, 0, len(headers))
	for _, header := range headers {
		base := filepath.Base(header)
		name := strings.TrimSuffix(base, "_types.h")
		if !validDomain(name) {
			return nil, fmt.Errorf("abi header %q has invalid domain %q", header, name)
		}
		domains = append(domains, domain{
			name:       name,
			prefix:     name + "_",
			headerPath: header,
			outputPath: filepath.Join(root, "internal", "bpf", "abi", name+"_types_generated.go"),
		})
	}
	return domains, nil
}

func validDomain(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if i > 0 && (r == '_' || r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func discoverCandidates(root string, domains []domain) (map[string][]candidate, error) {
	objects, err := filepath.Glob(filepath.Join(root, "bpf", "*.o"))
	if err != nil {
		return nil, fmt.Errorf("scan bpf objects: %w", err)
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("no bpf objects found under %q", filepath.Join(root, "bpf"))
	}
	sort.Strings(objects)

	byDomain := make(map[string][]candidate, len(domains))
	for _, object := range objects {
		spec, err := btf.LoadSpec(object)
		if err != nil {
			return nil, fmt.Errorf("load btf from %q: %w", object, err)
		}

		iter := spec.Iterate()
		for iter.Next() {
			v, ok := iter.Type.(*btf.Var)
			if !ok || !strings.HasPrefix(v.Name, anchorPrefix) {
				continue
			}

			rootType, err := anchoredType(v.Type)
			if err != nil {
				return nil, fmt.Errorf("object %q anchor %q: %w", object, v.Name, err)
			}
			cName := rootType.TypeName()
			d, ok := domainForType(domains, cName)
			if !ok {
				return nil, fmt.Errorf(
					"object %q anchor %q targets type %q without matching abi header",
					object,
					v.Name,
					cName,
				)
			}
			byDomain[d.name] = append(byDomain[d.name], candidate{
				cName:      cName,
				typ:        rootType,
				objectPath: object,
			})
		}
	}

	if err := validateCandidateCoverage(domains, byDomain); err != nil {
		return nil, err
	}
	return byDomain, nil
}

func validateCandidateCoverage(domains []domain, byDomain map[string][]candidate) error {
	for _, d := range domains {
		if len(byDomain[d.name]) != 0 {
			continue
		}
		return fmt.Errorf(
			"abi header %q has no %q btf anchors in bpf objects",
			d.headerPath,
			d.prefix,
		)
	}
	return nil
}

func anchoredType(typ btf.Type) (btf.Type, error) {
	typ = stripQualifiers(typ)
	ptr, ok := typ.(*btf.Pointer)
	if !ok {
		return nil, fmt.Errorf("type is %T, want pointer", typ)
	}
	typ = stripQualifiers(ptr.Target)
	if typ.TypeName() == "" {
		return nil, fmt.Errorf("target %T is unnamed", typ)
	}
	return typ, nil
}

func domainForType(domains []domain, name string) (domain, bool) {
	var matched domain
	for _, d := range domains {
		if !strings.HasPrefix(name, d.prefix) {
			continue
		}
		if matched.name != "" {
			return domain{}, false
		}
		matched = d
	}
	return matched, matched.name != ""
}

func ensureOutputDir(root string) error {
	path := filepath.Join(root, "internal", "bpf", "abi")
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", path, err)
	}
	return nil
}
