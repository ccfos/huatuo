import argparse
import contextlib
import os
import pathlib
import platform
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import textwrap
import threading

import memray
import sysconfig
from memray._errors import MemrayCommandError

from typing import Optional

try:
    from typing import Literal
except ImportError:
    from typing_extensions import Literal

from contextlib import closing

TrackingMode = Literal["ACTIVATE", "DEACTIVATE", "FOR_DURATION"]


GDB_SCRIPT = pathlib.Path(__file__).parent / "_attach.gdb"
LLDB_SCRIPT = pathlib.Path(__file__).parent / "_attach.lldb"
RTLD_DEFAULT = memray._memray.RTLD_DEFAULT
RTLD_NOW = memray._memray.RTLD_NOW
PAYLOAD = """
import atexit
import time
import threading
import resource
import sys
from contextlib import suppress

PYTHON_PATHS = {python_paths!r}
for _p in PYTHON_PATHS or []:
    if _p and _p not in sys.path:
        sys.path.insert(0, _p)

import memray


class BareExceptionMessage(Exception):
    def __repr__(self):
        return self.args[0]


class RepeatingTimer(threading.Thread):
    def __init__(self, interval, function):
        self._interval = interval
        self._function = function
        self._canceled = threading.Event()
        super().__init__()

    def cancel(self):
        self._canceled.set()

    def run(self):
        while not self._canceled.wait(self._interval):
            if self._function():
                break


def deactivate_last_tracker():
    tracker = getattr(memray, "_last_tracker", None)
    if not tracker:
        return

    memray._last_tracker = None
    try:
        tracker.__exit__(None, None, None)
    finally:
        # Clean up resources associated with the Tracker ASAP,
        # even if an exception was raised.
        del tracker

    # Stop any waiting threads. This attribute may be unset if an old Memray
    # version attached 1st, setting last_tracker but not _attach_event_threads.
    # It could also be unset if we're racing another deactivate call.
    for thread in memray.__dict__.pop("_attach_event_threads", []):
        thread.cancel()


def activate_tracker():
    deactivate_last_tracker()
    tracker = {tracker_call}
    try:
        tracker.__enter__()
        memray._last_tracker = tracker
    finally:
        # Clean up resources associated with the Tracker ASAP,
        # even if an exception was raised.
        del tracker
    memray._attach_event_threads = []


def track_for_duration(duration=5):
    activate_tracker()

    def deactivate_because_timer_elapsed():
        print(
            "memray: Deactivating tracking:",
            duration,
            "seconds have elapsed",
            file=sys.stderr,
        )
        deactivate_last_tracker()

    thread = threading.Timer(duration, deactivate_because_timer_elapsed)
    thread.daemon = False
    thread.start()
    memray._attach_event_threads.append(thread)


if not hasattr(memray, "_last_tracker"):
    # This only needs to be registered the first time we attach.
    atexit.register(deactivate_last_tracker)


def _get_free_port() -> int:
    with closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as sock:
        sock.bind(("", 0))
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        return int(sock.getsockname()[1])

if {mode!r} == "ACTIVATE":
    activate_tracker()
elif {mode!r} == "DEACTIVATE":
    if not getattr(memray, "_last_tracker", None):
        raise BareExceptionMessage("no previous `memray attach` call detected")
    deactivate_last_tracker()
elif {mode!r} == "FOR_DURATION":
    track_for_duration({duration})
"""


def _resolve_injector_path(injector_path: Optional[str]) -> pathlib.Path:
    if injector_path:
        return pathlib.Path(injector_path)

    pkg_dir = pathlib.Path(memray.__file__).parent
    soabi = sysconfig.get_config_var("SOABI") or ""
    if soabi:
        abi_candidate = pkg_dir / ("_inject.%s.so" % soabi)
        if abi_candidate.exists():
            return abi_candidate
        try:
            from memray import _inject as _inject_mod  # type: ignore
            return pathlib.Path(_inject_mod.__file__)
        except Exception:
            candidates = sorted(pkg_dir.glob("_inject*.so"))
    else:
        candidates = sorted(pkg_dir.glob("_inject*.so"))

    if not candidates:
        raise MemrayCommandError("Cannot locate memray _inject module", exit_code=1)
    non_abi3 = [c for c in candidates if ".abi3." not in c.name]
    return non_abi3[0] if non_abi3 else candidates[0]


def debugger_inject(
    debugger: str,
    pid: int,
    port: int,
    verbose: bool,
    injector_path: Optional[str] = None,
) -> Optional[str]:
    """Execute a file in a running Python process using a debugger."""
    injecter = _resolve_injector_path(injector_path)

    gdb_cmd = [
        "gdb",
        "-batch",
        "-p",
        str(pid),
        "-nx",
        "-nw",
        "-iex=set auto-solib-add off",
        f"-ex=set $rtld_now={RTLD_NOW}",
        f'-ex=set $libpath="{injecter}"',
        f"-ex=set $port={port}",
        f"-x={GDB_SCRIPT}",
    ]

    lldb_cmd = [
        "lldb",
        "--batch",
        "-p",
        str(pid),
        "--no-lldbinit",
        "-o",
        f'expr char $libpath[]="{injecter}"',
        "-o",
        f"expr int $port={port}",
        "-o",
        f"expr void* $rtld_default=(void*){RTLD_DEFAULT}",
        "-o",
        f"expr int $rtld_now={RTLD_NOW}",
        "--source",
        f"{LLDB_SCRIPT}",
    ]

    cmd = gdb_cmd if debugger == "gdb" else lldb_cmd
    if verbose:
        if sys.version_info >= (3, 8):
            print("Debugger command line:", shlex.join(cmd))
        else:
            print("Debugger command line:", cmd)

    try:
        # Python 3.7+: 'text' kw; Python 3.6: use universal_newlines
        if sys.version_info[:2] >= (3, 7):
            output = subprocess.check_output(cmd, text=True, stderr=subprocess.STDOUT)
        else:
            output = subprocess.check_output(
                cmd, universal_newlines=True, stderr=subprocess.STDOUT
            )
        returncode = 0
    except subprocess.CalledProcessError as exc:
        output = exc.output
        returncode = exc.returncode

    if cmd is lldb_cmd:
        # A bug in lldb sometimes means processes stay stopped after it exits.
        # Send a signal to wake the process up. Ignore any errors: the process
        # may have died, or may have never existed, or may be owned by another
        # user, etc. Processes that aren't stopped will ignore this signal, so
        # this should be harmless, though it is a huge hack.
        with contextlib.suppress(OSError):
            os.kill(pid, signal.SIGCONT)

    if verbose:
        print(f"debugger return code: {returncode}")
        print(f"debugger output:\n{output}")

    # On older environments, gdb output may not include the SUCCESS marker even if
    # injection succeeded. Treat a zero return code as success and rely on the
    # side-channel accept to fail if injection truly didn't happen.
    if returncode == 0:
        return None

    # An error occurred. Give the best message we can. This is hacky; we don't
    # have a good option besides parsing output from the debugger session.
    if "--help" in output:
        return (
            "The debugger failed to parse our command line arguments.\n"
            "Run with --verbose to see the error message."
        )

    if "error: attach failed: " in output or "ptrace: " in output:
        # We failed to attach to the given pid. A few likely reasons...
        errmsg = "Failed to attach a debugger to the process.\n"
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return errmsg + "The given process ID does not exist."
        except PermissionError:
            return errmsg + "The given process ID is owned by a different user."

        return errmsg + "You most likely do not have permission to trace the process."

    if "MEMRAY: Attached to process." not in output:
        return (
            f"Failed to execute our {debugger} script.\n"
            "Run with --verbose to debug the failure."
        )

    if "MEMRAY: Checking if process is Python 3.7+." in output:
        if "MEMRAY: Process is Python 3.7+." not in output:
            return "The process does not seem to be running Python 3.7 or newer."

    return "An unexpected error occurred. Run with --verbose to debug the failure."


def remote_exec_inject(pid: int, port: int, verbose: bool, tmpdir: str) -> Optional[str]:
    """Execute a file in a running Python process using sys.remote_exec."""
    script = textwrap.dedent(
        f"""
        from contextlib import closing
        from queue import Queue
        from socket import create_connection
        from threading import Thread

        exc = None

        def thread_body():
            try:
                exec(sockfile.read(), globals())
            except Exception as exc:
                globals()["exc"] = exc

        with closing(create_connection((None, {port})).makefile("rw")) as sockfile:
            thread = Thread(target=thread_body)
            thread.start()
            thread.join()
            if exc is not None:
                sockfile.write(repr(exc))
        """
    )
    script_path = pathlib.Path(tmpdir) / "attach.py"
    script_path.write_text(script, encoding="utf-8")
    try:
        getattr(sys, "remote_exec")(pid, script_path)
    except Exception as exc:
        return f"Failed to execute script in remote process: {exc!r}"
    return None


def inject(
    method: str,
    pid: int,
    port: int,
    verbose: bool,
    tmpdir: str,
    injector_path: Optional[str] = None,
) -> Optional[str]:
    """Execute a file in a running Python process."""
    if method == "sys.remote_exec":
        return remote_exec_inject(pid, port, verbose=verbose, tmpdir=tmpdir)
    return debugger_inject(
        method,
        pid,
        port,
        verbose=verbose,
        injector_path=injector_path,
    )


def _sys_remote_exec_available(verbose: bool) -> bool:
    if not hasattr(sys, "remote_exec"):
        if verbose:
            print("sys.remote_exec is not available in this Python version")
        return False
    return True


def _gdb_available(verbose: bool) -> bool:
    if not shutil.which("gdb"):
        if verbose:
            print("No gdb executable found")
        return False
    return True


def _lldb_available(verbose: int) -> bool:
    # We need a version of lldb that supports `--batch`. This should be lldb
    # 3.5.2 or newer, but the version string format doesn't appear consistent
    # between macOS and Linux, so it's safer to just check the help output to
    # make sure that option is documented.
    try:
        if sys.version_info[:2] >= (3, 7):
            help_str = subprocess.check_output(
                ["lldb", "--help"], text=True, stderr=subprocess.STDOUT
            )
        else:
            help_str = subprocess.check_output(
                ["lldb", "--help"], universal_newlines=True, stderr=subprocess.STDOUT
            )
    except FileNotFoundError:
        if verbose:
            print("No lldb executable found")
        return False
    except subprocess.CalledProcessError as exc:
        if verbose:
            print(f"`lldb --version` failed: {exc.output}")
        return False

    if "--batch" not in help_str:
        if verbose:
            print("lldb does not support --batch, which we require")
        return False

    return True


def debugger_available(debugger: str, verbose: bool = False) -> bool:
    return {
        "sys.remote_exec": _sys_remote_exec_available,
        "gdb": _gdb_available,
        "lldb": _lldb_available,
    }[debugger](verbose=verbose)


def recvall(sock: socket.socket) -> str:
    try:
        return b"".join(iter(lambda: sock.recv(4096), b""))
    except OSError:
        return ""


class ErrorReaderThread(threading.Thread):
    def __init__(self, sock):
        self._sock = sock
        super().__init__()

    def run(self):
        try:
            err = recvall(self._sock)
        except OSError as e:
            err = f"Unexpected exception: {e!r}"

        if not err:
            self.error = None
            return

        self.error = err
        os.kill(os.getpid(), signal.SIGINT)


def render_payload(tracker_call, mode, duration, python_paths=None):
    return PAYLOAD.format(
        tracker_call=tracker_call,
        mode=mode,
        duration=duration,
        python_paths=python_paths or [],
    )


class _DebuggerCommand:
    def prepare_parser(self, parser):
        parser.add_argument(
            "--method",
            help="Method to use for injecting commands into the remote process",
            type=str,
            default="auto",
            choices=["auto", "sys.remote_exec", "gdb", "lldb"],
        )

        parser.add_argument(
            "-v",
            "--verbose",
            help="Print verbose debugging information.",
            action="store_true",
        )

        parser.add_argument(
            "--injector-path",
            help="Override path to the memray _inject shared object",
            type=str,
        )

        parser.add_argument(
            "--target-pythonpath",
            action="append",
            default=None,
            help="Directory to prepend to the target's sys.path before importing memray (repeatable)",
        )

        parser.add_argument(
            "pid",
            help="Process id to affect",
            type=int,
        )

    def resolve_debugger(self, method, *, verbose=False):
        if method == "auto":
            # Prefer gdb on Linux but lldb on macOS
            if platform.system() == "Linux":
                debuggers = ("sys.remote_exec", "gdb", "lldb")
            else:
                debuggers = ("sys.remote_exec", "lldb", "gdb")

            for debugger in debuggers:
                if debugger_available(debugger, verbose=verbose):
                    return debugger
            raise MemrayCommandError(
                "Cannot find a supported lldb or gdb executable"
                " and sys.remote_exec is not available.",
                exit_code=1,
            )
        elif not debugger_available(method, verbose=verbose):
            raise MemrayCommandError(
                "sys.remote_exec requires Python 3.14 or newer."
                if method == "sys.remote_exec"
                else f"Cannot find a supported {method} executable.",
                exit_code=1,
            )
        return method

    def inject_control_channel(self, method, pid, *, verbose=False, injector_path=None):
        server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        with contextlib.closing(server), tempfile.TemporaryDirectory() as tmpdir:
            server.bind(("localhost", 0))
            server.listen(1)
            sidechannel_port = server.getsockname()[1]

            errmsg = inject(
                method,
                pid,
                sidechannel_port,
                verbose=verbose,
                tmpdir=tmpdir,
                injector_path=injector_path,
            )
            if errmsg:
                raise MemrayCommandError(errmsg, exit_code=1)

            server.settimeout(10)
            try:
                return server.accept()[0]
            except (socket.timeout, TimeoutError):
                raise MemrayCommandError(
                    f"Timed out waiting for connection from pid {pid}. Ensure the correct _inject*.so "
                    "was loaded and ptrace is permitted.",
                    exit_code=1,
                )


class AttachCommand(_DebuggerCommand):
    """Begin tracking allocations in an already-started process"""

    def prepare_parser(self, parser):
        parser.add_argument(
            "-o",
            "--output",
            metavar="FILE",
            help=(
                "Capture allocations into the given file"
                " instead of starting a live tracking session"
            ),
        )
        parser.add_argument(
            "--aggregate",
            help="Write aggregated stats to the output file instead of all allocations",
            action="store_true",
            default=False,
        )

        parser.add_argument(
            "--native",
            help="Track native (C/C++) stack frames as well",
            action="store_true",
            dest="native",
            default=False,
        )
        parser.add_argument(
            "--follow-fork",
            action="store_true",
            help="Record allocations in child processes forked from the tracked script",
            default=False,
        )
        parser.add_argument(
            "--trace-python-allocators",
            action="store_true",
            help="Record allocations made by the pymalloc allocator",
            default=False,
        )
        parser.add_argument(
            "--compress",
            dest="compress",
            action="store_true",
            default=False,
            help="Compress the resulting file with lz4 after tracking completes",
        )
        # Legacy flag kept for compatibility with older tooling (default already disables compression).
        parser.add_argument(
            "--no-compress",
            dest="compress",
            action="store_false",
            help=argparse.SUPPRESS,
        )

        parser.add_argument(
            "--duration", type=int, help="Duration to track for (in seconds)"
        )

        super().prepare_parser(parser)

    def run(self, args, parser):
        verbose = args.verbose
        mode: TrackingMode = "ACTIVATE"
        duration = None

        if args.duration:
            mode = "FOR_DURATION"
            duration = args.duration

        args.method = self.resolve_debugger(args.method, verbose=verbose)

        destination: memray.Destination
        if args.output:
            live_port = None
            destination = memray.FileDestination(
                path=os.path.abspath(args.output),
                overwrite=False,
                compress_on_exit=args.compress,
            )
        else:
            live_port = _get_free_port()
            destination = memray.SocketDestination(server_port=live_port)

        if args.aggregate and not args.output:
            parser.error("Can't use aggregated mode without an output file.")

        file_format = (
            "file_format=memray.FileFormat.AGGREGATED_ALLOCATIONS"
            if args.aggregate
            else ""
        )

        tracker_call = (
            f"memray.Tracker(destination=memray.{destination!r},"
            f" native_traces={args.native},"
            f" follow_fork={args.follow_fork},"
            f" trace_python_allocators={args.trace_python_allocators},"
            f"{file_format})"
        )

        client = self.inject_control_channel(
            args.method,
            args.pid,
            verbose=verbose,
            injector_path=args.injector_path,
        )
        payload = render_payload(tracker_call, mode, duration, args.target_pythonpath)
        client.sendall(payload.encode("utf-8"))
        client.shutdown(socket.SHUT_WR)

        if not live_port:
            err = recvall(client)
            if err:
                raise MemrayCommandError(
                    f"Failed to start tracking in remote process: {err}",
                    exit_code=1,
                )
            return

        # If an error prevents the tracked process from binding a server to
        # live_port, the TUI will hang forever trying to connect. Handle this
        # by spawning a background thread that watches for an error report over
        # the side channel and raises a SIGINT to interrupt the TUI if it sees
        # one. This can race, though: in some cases the TUI will also see an
        # error (if no header is sent over the socket), and the background
        # thread may raise a SIGINT that we see only after the live TUI has
        # already exited. If so we must ignore the extra KeyboardInterrupt.
        error_reader = ErrorReaderThread(client)
        error_reader.start()
        try:
            # Import the live TUI lazily to avoid importing textual on Python 3.6
            from .live import LiveCommand  # type: ignore
        except Exception:
            raise MemrayCommandError(
                "Live UI is unavailable. Install textual or pass -o to write to a file.",
                exit_code=1,
            )
        live = LiveCommand()

        with contextlib.suppress(KeyboardInterrupt):
            try:
                try:
                    live.start_live_interface(live_port)
                finally:
                    # Note: may get a spurious KeyboardInterrupt!
                    error_reader.join()
            except (Exception, KeyboardInterrupt):
                remote_err = error_reader.error
                if not remote_err:
                    raise  # Propagate the exception

                raise MemrayCommandError(
                    f"Failed to start tracking in remote process: {remote_err}",
                    exit_code=1,
                ) from None


class DetachCommand(_DebuggerCommand):
    """End the tracking started by a previous ``memray attach`` call"""

    def run(self, args, parser):
        verbose = args.verbose
        mode: TrackingMode = "DEACTIVATE"
        args.method = self.resolve_debugger(args.method, verbose=verbose)
        client = self.inject_control_channel(
            args.method,
            args.pid,
            verbose=verbose,
            injector_path=args.injector_path,
        )

        payload = render_payload(None, mode, None, args.target_pythonpath)
        client.sendall(payload.encode("utf-8"))
        client.shutdown(socket.SHUT_WR)

        err = recvall(client)
        if err:
            raise MemrayCommandError(
                f"Failed to stop tracking in remote process: {err}",
                exit_code=1,
            )
