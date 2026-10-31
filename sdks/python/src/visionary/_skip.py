"""Settings -> Data skip list: ``requests.skip`` in ``visionary_settings``, a
JSON array of exact paths, or prefixes ending in ``*``. Matching requests are
treated like paths outside VISIONARY_LOG_PATH_PREFIX.
"""

from __future__ import annotations

import json
import logging
import threading
from typing import Any, List, Optional, Sequence

from . import _spans

SETTING = "requests.skip"
SETTINGS_POLL_S = 30.0


def parse(value: str) -> List[str]:
    """The stored value; anything but a JSON array of strings skips nothing."""
    try:
        lst = json.loads(value)
    except ValueError:
        return []
    if not isinstance(lst, list) or not all(isinstance(p, str) for p in lst):
        return []
    return [p for p in (p.strip() for p in lst) if p]


def match(lst: Sequence[str], path: str) -> bool:
    """Whether path equals an entry, or starts with one ending in ``*``."""
    for p in lst:
        if p.endswith("*"):
            if path.startswith(p[:-1]):
                return True
        elif path == p:
            return True
    return False


class SkipList:
    """Keeps the skip list fresh on a daemon thread."""

    def __init__(self, backend: Any, logger: logging.Logger, poll_s: float = SETTINGS_POLL_S) -> None:
        self._backend = backend
        self._logger = logger
        self._poll_s = poll_s
        self.paths: List[str] = []
        self._init_state()

    def _init_state(self) -> None:
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._read_failed = False

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="visionary-skip-settings", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self._init_state()
        self.start()

    def close(self) -> None:
        self._stop.set()

    def _run(self) -> None:
        _spans.mark_internal()
        while not self._stop.is_set():
            self.poll()
            self._stop.wait(self._poll_s)

    def poll(self) -> None:
        """Read Settings -> Data; a failed read keeps the last list."""
        try:
            rows = self._backend.read_settings("requests")
        except Exception as e:
            # No server or table yet.
            if not self._read_failed:
                self._read_failed = True
                self._logger.debug("visionary: request skip list unavailable (%s)", e)
            return
        self._read_failed = False
        paths: List[str] = []
        for r in rows:
            if len(r) >= 2 and r[0] == SETTING:
                paths = parse(r[1])
        self.paths = paths

    def skipped(self, path: str) -> bool:
        return match(self.paths, path)
