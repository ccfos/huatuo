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

package output

import (
	"context"
	"io"

	"github.com/ccfos/huatuo/pkg/types"
)

type localFormatter struct {
	renderer Renderer
	writer   io.Writer
	finish   func(Renderer) error
}

func (f *localFormatter) Add(_ context.Context, window *types.ProfilingWindow) error {
	return AddProfile(f.renderer, window.Profile)
}

func (f *localFormatter) Complete(context.Context) error {
	if f.writer != nil {
		return f.renderer.Write(f.writer)
	}
	if f.renderer.IsEmpty() {
		return nil
	}
	return f.finish(f.renderer)
}
