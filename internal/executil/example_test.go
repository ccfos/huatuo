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

package executil_test

import (
	"context"
	"fmt"

	"github.com/ccfos/huatuo/internal/executil"
)

func ExampleRun() {
	result, err := executil.Run(context.Background(), executil.Spec{Path: "/bin/echo", Args: []string{"ready"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(string(result.Stdout))
	// Output: ready
}

func ExampleProcess_Done() {
	process, err := executil.New(executil.Spec{Path: "/bin/echo", Args: []string{"ready"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() {
		if err := process.Close(); err != nil {
			fmt.Println(err)
		}
	}()
	if err := process.Start(context.Background()); err != nil {
		fmt.Println(err)
		return
	}
	<-process.Done()
	if err := process.Wait(); err != nil {
		fmt.Println(err)
		return
	}
	output, err := process.Stdout()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(string(output))
	// Output: ready
}
