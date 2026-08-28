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

package console

import (
	"embed"
	"io/fs"
)

// embeddedWeb holds the compiled web console assets. The directory is served by
// the apiserver at /console and is exempt from authentication so the login
// screen can be displayed before a credential is available.
//
//go:embed web
var embeddedWeb embed.FS

// WebFS returns the root filesystem of the embedded web console assets.
func WebFS() fs.FS {
	sub, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		// fs.Sub only fails if "web" is not a valid sub-tree; the //go:embed
		// directive above guarantees it exists at compile time.
		panic(err)
	}
	return sub
}
