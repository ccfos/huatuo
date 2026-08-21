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

public final class TestProfilerJavaMemory {
    private static final byte[][] RETAINED = new byte[4096][];
    private static int retainedIndex;

    private TestProfilerJavaMemory() {}

    public static void main(String[] args) {
        while (true) {
            allocateHotMethod();
        }
    }

    private static void allocateHotMethod() {
        for (int i = 0; i < 1024; i++) {
            byte[] value = new byte[4096];
            if ((i & 15) == 0) {
                RETAINED[retainedIndex++ & (RETAINED.length - 1)] = value;
            }
        }
    }
}
