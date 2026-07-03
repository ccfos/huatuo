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

package fileutil

import (
	"os"
	"testing"
)

func TestStatInodeSuccess(t *testing.T) {
	f, err := os.CreateTemp("", "huatuo-test-*")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer os.Remove(f.Name())

	ino, err := StatInode(f.Name())
	if err != nil {
		t.Fatalf("StatInode(%q) error = %v", f.Name(), err)
	}
	if ino == 0 {
		t.Errorf("StatInode(%q) = 0, want non-zero", f.Name())
	}
}

func TestStatInodeMissingFile(t *testing.T) {
	_, err := StatInode("/does/not/exist/huatuo-test-missing")
	if err == nil {
		t.Fatal("StatInode() error = nil, want non-nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("StatInode() error = %v, want os.IsNotExist error", err)
	}
}
