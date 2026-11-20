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

package pcie

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var validLinkFiles = map[string]string{
	"max_link_speed":     "32.0 GT/s\n",
	"max_link_width":     "16\n",
	"current_link_speed": "16.0 GT/s\n",
	"current_link_width": "8\n",
}

func TestGetPCIeLinkInfo(t *testing.T) {
	base := writeLinkFiles(t, validLinkFiles)

	info, err := getPCIeLinkInfo(base)
	if err != nil {
		t.Fatalf("getPCIeLinkInfo() error = %v", err)
	}
	want := PCIeLinkInfo{CapSpeed: 32, CapWidth: 16, StatusSpeed: 16, StatusWidth: 8}
	if *info != want {
		t.Errorf("getPCIeLinkInfo() = %+v, want %+v", *info, want)
	}
}

func TestGetPCIeLinkInfoRejectsIncompleteSample(t *testing.T) {
	tests := []struct {
		name        string
		missing     string
		wantErrPart string
	}{
		{name: "maximum speed", missing: "max_link_speed", wantErrPart: "maximum link speed"},
		{name: "maximum width", missing: "max_link_width", wantErrPart: "maximum link width"},
		{name: "current speed", missing: "current_link_speed", wantErrPart: "current link speed"},
		{name: "current width", missing: "current_link_width", wantErrPart: "current link width"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := cloneLinkFiles()
			delete(files, tt.missing)
			_, err := getPCIeLinkInfo(writeLinkFiles(t, files))
			if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Fatalf("getPCIeLinkInfo() error = %v, want error containing %q", err, tt.wantErrPart)
			}
		})
	}
}

func TestGetPCIeLinkInfoRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name        string
		file        string
		value       string
		wantErrPart string
	}{
		{name: "non-numeric maximum speed", file: "max_link_speed", value: "unknown\n", wantErrPart: "maximum link speed"},
		{name: "NaN current speed", file: "current_link_speed", value: "NaN GT/s\n", wantErrPart: "current link speed"},
		{name: "zero maximum width", file: "max_link_width", value: "0\n", wantErrPart: "maximum link width"},
		{name: "non-numeric current width", file: "current_link_width", value: "x8\n", wantErrPart: "current link width"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := cloneLinkFiles()
			files[tt.file] = tt.value
			_, err := getPCIeLinkInfo(writeLinkFiles(t, files))
			if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Fatalf("getPCIeLinkInfo() error = %v, want error containing %q", err, tt.wantErrPart)
			}
		})
	}
}

func cloneLinkFiles() map[string]string {
	files := make(map[string]string, len(validLinkFiles))
	for name, value := range validLinkFiles {
		files[name] = value
	}
	return files
}

func writeLinkFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	base := t.TempDir()
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(base, name), []byte(value), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return base
}
