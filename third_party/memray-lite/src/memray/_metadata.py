import typing
try:
    from dataclasses import dataclass
except Exception:
    # Python 3.6: provide a no-op fallback if dataclasses backport isn't available
    def dataclass(cls=None, **kwargs):
        def wrap(c):
            return c
        return wrap(cls) if cls is not None else wrap
from datetime import datetime

if typing.TYPE_CHECKING:
    from ._memray import FileFormat


@dataclass
class Metadata(object):
    # Use comments for type hints for Python 3.6 compatibility
    # start_time: datetime
    # end_time: datetime
    # total_allocations: int
    # total_frames: int
    # peak_memory: int
    # command_line: str
    # pid: int
    # main_thread_id: int
    # python_allocator: str
    # has_native_traces: bool
    # trace_python_allocators: bool
    # file_format: FileFormat
    start_time = None  # type: datetime
    end_time = None  # type: datetime
    total_allocations = 0  # type: int
    total_frames = 0  # type: int
    peak_memory = 0  # type: int
    command_line = ""  # type: str
    pid = 0  # type: int
    main_thread_id = 0  # type: int
    python_allocator = ""  # type: str
    has_native_traces = False  # type: bool
    trace_python_allocators = False  # type: bool
    file_format = None  # type: 'FileFormat'
