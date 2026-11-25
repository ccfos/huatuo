# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Close a toolstream peer after receiving its connection handshake."""

import pathlib
import socket
import sys

with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as server:
    server.bind(sys.argv[1])
    server.listen(1)
    server.settimeout(20)
    pathlib.Path(sys.argv[3]).touch()
    peer, _ = server.accept()
    with peer:
        peer.settimeout(5)
        peer.recv(4096)
pathlib.Path(sys.argv[2]).touch()
