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

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/runmode"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestSandboxDocumentNodeIdentity(t *testing.T) {
	opts := &Options{Mode: runmode.Sandbox, Region: "test", GuestIdentity: runmode.GuestIdentity{NodeName: "physical-node"}}
	metadata, err := newDocumentBuilder(opts).Build(&document.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Hostname != "physical-node" || metadata.Region != "test" || metadata.SourceType != types.DocumentSourceKataGuest {
		t.Errorf("Guest document metadata=%+v", metadata)
	}
}

func TestHostDocumentOrigin(t *testing.T) {
	metadata, err := newDocumentBuilder(&Options{Mode: runmode.Baremetal}).Build(&document.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SourceType != types.DocumentSourceHost {
		t.Errorf("Host origin=%q", metadata.SourceType)
	}
}
