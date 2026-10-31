"""Visionary collector SDK.

Logs one row per HTTP request into ``api_logs``, the database and outbound
HTTP calls each request makes into ``spans``, unhandled and reported
exceptions into ``errors``, and samples host, process and runtime metrics
into ``system_metrics``, what the app logs through ``logging`` into
``app_logs`` (``visionary.capture_logging()``), and custom metrics
(``visionary.counter("orders", region="eu").add(1)``) into ``metrics``, from
background threads. Requests and outbound calls carry W3C trace context.

    import visionary
    agent = visionary.init()            # from VISIONARY_* / D1_* env vars

Then add the integration for your framework (``visionary.integrations.django``,
``.asgi`` or ``.flask``) and call ``visionary.set_user(id)`` once the user is
known. See README.md and SPEC.md.
"""

from ._agent import Agent, get_agent, init
from ._api import background, background_span, capture_exception, instrument_sqlalchemy, span
from ._config import Config, from_env
from ._context import login_attempt, set_route, set_user
from ._db import instrument_connection, instrument_cursor
from ._http import instrument_http
from ._loghandler import LogHandler, capture_logging
from ._metrics import Counter, Gauge, Histogram, counter, describe_metric, gauge, histogram
from ._writer import WriterStats

__version__ = "0.2.0"

__all__ = [
    "Agent", "Config", "Counter", "Gauge", "Histogram", "LogHandler", "WriterStats", "background",
    "background_span", "capture_exception", "capture_logging", "counter", "describe_metric", "from_env", "gauge",
    "get_agent",
    "histogram", "init", "instrument_connection", "instrument_cursor", "instrument_http", "instrument_sqlalchemy",
    "login_attempt", "set_route", "set_user", "span", "__version__",
]
