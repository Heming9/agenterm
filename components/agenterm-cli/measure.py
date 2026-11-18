#!/usr/bin/env python3
"""Report actual executable sizes, independent of archive/container overhead."""
import hashlib
import json
import lzma
from pathlib import Path
import sys

paths = sys.argv[1:] or [str(Path(__file__).parent / "target/release/agenterm-cli")]
for name in paths:
    path = Path(name).resolve()
    data = path.read_bytes()
    print(json.dumps({
        "file": str(path),
        "bytes": len(data),
        "KiB": round(len(data) / 1024, 2),
        "xz_bytes": len(lzma.compress(data, preset=6)),
        "sha256": hashlib.sha256(data).hexdigest(),
    }))
