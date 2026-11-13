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

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.lang.management.ManagementFactory;
import java.lang.management.ThreadMXBean;

public final class TestProfilerJavaMultiPID {
    private static volatile long sink;

    private TestProfilerJavaMultiPID() {}

    public static void main(String[] args) throws IOException {
        if (args.length < 1 || args.length > 2
            || (args.length == 2 && !args[1].equals("controlled"))) {
            throw new IllegalArgumentException("expected workload name [controlled]");
        }

        System.out.println("ready");
        System.out.flush();
        if (args.length == 2) {
            controlledWorkload(args[0]);
            return;
        }
        switch (args[0]) {
            case "alpha":
                alphaWorkload();
                break;
            case "beta":
                betaWorkload();
                break;
            default:
                throw new IllegalArgumentException(
                    "unsupported workload: " + args[0]
                );
        }
    }

    private static void alphaWorkload() {
        while (true) {
            alphaHotMethod();
        }
    }

    private static void betaWorkload() {
        while (true) {
            betaHotMethod();
        }
    }

    private static void controlledWorkload(String workload) throws IOException {
        if (!workload.equals("alpha") && !workload.equals("beta")) {
            throw new IllegalArgumentException("unsupported workload: " + workload);
        }
        BufferedReader commands = new BufferedReader(new InputStreamReader(System.in));
        ThreadMXBean cpuTime = ManagementFactory.getThreadMXBean();
        if (!cpuTime.isCurrentThreadCpuTimeSupported()) {
            throw new IllegalStateException("thread CPU time is unavailable");
        }
        if (!cpuTime.isThreadCpuTimeEnabled()) {
            cpuTime.setThreadCpuTimeEnabled(true);
        }
        boolean afterFault = false;
        while (true) {
            if (commands.ready()) {
                String command = commands.readLine();
                if ("after-fault".equals(command) && workload.equals("beta")) {
                    afterFault = true;
                } else if (!"checkpoint".equals(command)) {
                    throw new IllegalArgumentException("unsupported command: " + command);
                }
                // Acknowledge actual CPU work after attachment or the phase switch.
                // CPU time avoids depending on how much wall time CI scheduling takes.
                long deadline = cpuTime.getCurrentThreadCpuTime() + 200_000_000L;
                do {
                    controlledHotMethod(workload, afterFault);
                } while (cpuTime.getCurrentThreadCpuTime() < deadline);
                System.out.println(command);
                System.out.flush();
            }
            controlledHotMethod(workload, afterFault);
        }
    }

    private static void controlledHotMethod(String workload, boolean afterFault) {
        if (workload.equals("alpha")) {
            alphaHotMethod();
        } else if (afterFault) {
            betaAfterFaultHotMethod();
        } else {
            betaHotMethod();
        }
    }

    private static void alphaHotMethod() {
        for (int i = 0; i < 100_000; i++) {
            sink += i;
        }
    }

    private static void betaHotMethod() {
        for (int i = 0; i < 100_000; i++) {
            sink ^= i;
        }
    }

    private static void betaAfterFaultHotMethod() {
        for (int i = 0; i < 100_000; i++) {
            sink ^= i;
        }
    }
}
