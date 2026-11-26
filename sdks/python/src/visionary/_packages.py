"""Installed distributions for app_packages (SPEC "Packages")"""

from __future__ import annotations

import logging
import threading
import time
from importlib import metadata
from typing import Any, Iterable, List, Optional, Tuple

from ._entry import format_ts
from ._storage import Backend

DELAY = 30.0
MAX_PACKAGES = 5000


def distributions() -> List[Tuple[str, str]]:
    out = []
    for d in metadata.distributions():
        try:
            out.append((d.metadata["Name"] or "", d.version or ""))
        except Exception:
            continue
    return out


def package_rows(pkgs: Iterable[Tuple[str, str]], created_at: str, service: Optional[str], instance: str,
                 version: Optional[str]) -> List[Tuple[Any, ...]]:
    seen = set()
    rows: List[Tuple[Any, ...]] = []
    for name, ver in pkgs:
        if not name or not ver or (name, ver) in seen:
            continue
        seen.add((name, ver))
        rows.append((created_at, service, instance, version, "PyPI", name, ver))
        if len(rows) == MAX_PACKAGES:
            break
    return rows


class PackageReport:
    """Writes app_packages once, a while after start, on a daemon thread"""

    def __init__(self, backend: Backend, logger: logging.Logger, service: str, instance: str, version: str,
                 delay: float = DELAY) -> None:
        self._backend = backend
        self._logger = logger
        self._service = service or None
        self._instance = instance
        self._version = version or None
        self._delay = delay
        self._timer: Optional[threading.Timer] = None

    def start(self) -> None:
        self._timer = threading.Timer(self._delay, self.run)
        self._timer.name = "visionary-packages"
        self._timer.daemon = True
        self._timer.start()

    def restart_after_fork(self) -> None:
        self.start()

    def close(self) -> None:
        if self._timer is not None:
            self._timer.cancel()

    def run(self) -> None:
        try:
            rows = package_rows(distributions(), format_ts(time.time()), self._service, self._instance,
                                self._version)
            if rows:
                self._backend.create_schema("telemetry")
                self._backend.insert("packages", rows)
        except Exception as e:
            self._logger.error("visionary: write packages failed: %s", e)
