# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

import ctypes
import json
import os
import signal
import sys
import time


def retain_memory():
    return native.memray_test_retain()


def temporary_memory():
    return native.memray_test_temporary()


def mapped_memory():
    return native.memray_test_map()


def allocate_workload():
    retained = retain_memory()
    temporary = temporary_memory()
    mapping = mapped_memory()
    if not retained or not temporary or mapping == ctypes.c_void_p(-1).value:
        raise RuntimeError("fixture allocation failed")
    return retained, temporary, mapping


def request_phase(signum, frame):
    # Allocation in a handler would add signal-dispatch frames to the trace.
    global phase_requested
    phase_requested = True


def request_startup_cancel(signum, frame):
    global startup_cancel_requested
    startup_cancel_requested = True


def arm_startup_cancel(memray, control_path):
    original_tracker = memray.Tracker

    def blocked_tracker(*args, **kwargs):
        memray.Tracker = original_tracker
        with open(control_path) as control:
            profiler_pid = int(control.read())
        payload = sys._getframe(1).f_globals
        # Freeze the reader before the real SocketDestination opens. Shell
        # waits for LISTEN before writing the payload's cancellation marker.
        os.kill(profiler_pid, signal.SIGSTOP)
        deadline = time.monotonic() + 2
        while True:
            with open("/proc/%d/status" % profiler_pid) as status:
                state = next(line for line in status if line.startswith("State:"))
            if state.split()[1] == "T":
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("profiler did not stop before opening the data listener")
            time.sleep(0.01)
        with open(control_path + ".waiting", "w") as waiting:
            json.dump({"port": payload["LIVE_PORT"],
                       "cancel_path": payload["CANCEL_PATH"]}, waiting)
        print("startup-waiting", flush=True)
        return original_tracker(*args, **kwargs)

    memray.Tracker = blocked_tracker
    print("startup-cancel-armed", flush=True)


native = ctypes.CDLL(sys.argv[1])
for name in ("memray_test_retain", "memray_test_temporary", "memray_test_map"):
    function = getattr(native, name)
    function.argtypes = []
    function.restype = ctypes.c_void_p
native.memray_test_release.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
native.memray_test_release.restype = ctypes.c_int
native.memray_test_discard.argtypes = [ctypes.c_void_p] * 3
native.memray_test_discard.restype = ctypes.c_int

# Warm ctypes and Python frame bookkeeping before hooks are installed.
if native.memray_test_discard(*allocate_workload()) != 0:
    raise RuntimeError("fixture warmup munmap failed")

phase_requested = False
startup_cancel_requested = False
phase = 0
was_tracking = False
signal.signal(signal.SIGUSR1, request_phase)
signal.signal(signal.SIGUSR2, request_startup_cancel)
print("ready", flush=True)

while True:
    # Do not import Memray before injection: the profiler must stage its bundle.
    memray = sys.modules.get("memray")
    if startup_cancel_requested:
        startup_cancel_requested = False
        if memray is None or len(sys.argv) != 3:
            raise RuntimeError("startup cancellation requires a previous attach and control path")
        arm_startup_cancel(memray, sys.argv[2])
    tracking = bool(getattr(memray, "_last_tracker", None))
    if tracking != was_tracking:
        print("tracking" if tracking else "deactivated", flush=True)
        was_tracking = tracking
    if phase_requested:
        phase_requested = False
        if not tracking:
            raise RuntimeError("fixture phase requested outside tracking")
        if phase == 0:
            retained, temporary, mapping = allocate_workload()
            print("allocated", flush=True)
        elif phase == 1:
            if native.memray_test_release(temporary, mapping) != 0:
                raise RuntimeError("fixture middle-half munmap failed")
            print("released", flush=True)
        else:
            raise RuntimeError("unexpected fixture phase")
        phase += 1
    time.sleep(0.01)
