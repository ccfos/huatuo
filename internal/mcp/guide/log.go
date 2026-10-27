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

package guide

import (
	"github.com/ccfos/huatuo/internal/log"
)

const (
	// LogTag prefixes guide logs; the trailing space separates it from messages.
	LogTag = "[mcp-guide] "
)

func infof(format string, args ...any)  { log.Infof(LogTag+format, args...) }
func debugf(format string, args ...any) { log.Debugf(LogTag+format, args...) }
func warnf(format string, args ...any)  { log.Warnf(LogTag+format, args...) }
func errorf(format string, args ...any) { log.Errorf(LogTag+format, args...) }
