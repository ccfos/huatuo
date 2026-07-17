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

package context

import (
	"bytes"
	"flag"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

func TestProfilerContextCancelStopsSignalListener(t *testing.T) {
	set := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	set.String("type", "cpu", "")
	set.String("language", "c", "")
	set.String("output-format", "collapsed", "")
	set.String("tracer-id", "trace-123", "")
	cliCtx := cli.NewContext(nil, set, nil)

	pctx, err := NewProfilerContext(cliCtx, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("NewProfilerContext() error = %v", err)
	}
	if pctx.TracerID != "trace-123" {
		t.Fatalf("TracerID = %q, want trace-123", pctx.TracerID)
	}
	pctx.Cancel()
	pctx.Cancel()

	select {
	case <-pctx.Ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ProfilerContext.Cancel() did not cancel context")
	}
}
