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
	"testing"

	"huatuo-bamai/cmd/huatuo-apiserver/config"
)

func TestSetupProfileFlamegraphSkipsDisabledStorage(t *testing.T) {
	daemon := &Daemon{
		opts: &Options{Config: &config.Config{}},
	}

	cleanup, err := setupProfileFlamegraph(t.Context(), daemon)
	if err != nil {
		t.Fatalf("setupProfileFlamegraph() error = %v", err)
	}
	if cleanup != nil {
		t.Error("setupProfileFlamegraph() cleanup is not nil")
	}
	if daemon.profileService != nil {
		t.Error("profileService is initialized when storage is disabled")
	}
}
