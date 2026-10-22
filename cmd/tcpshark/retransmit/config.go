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

package retransmit

import (
	"io"

	"github.com/ccfos/huatuo/internal/dropwatch"
)

// Config configures one TCP retransmit tracing instance.
type Config struct {
	BPFPath          string
	FilterExpression string
	// MaxEventsPerSecond is zero for unlimited emission.
	MaxEventsPerSecond uint64
	TLPEnabled         bool
}

// RunConfig configures a retransmit session. Dropwatch == nil disables
// correlation. Output is borrowed; the session only closes its socket client.
type RunConfig struct {
	Tracing       Config
	Dropwatch     *dropwatch.Config
	SourceType    string
	Output        io.Writer
	OutputFormat  string
	OutputStorage string
	ToolName      string
	Version       string
	TaskID        string
}
