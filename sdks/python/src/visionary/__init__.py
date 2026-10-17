"""Visionary collector SDK.

Logs one row per HTTP request into ``api_logs`` and samples host, process and
runtime metrics into ``system_metrics``, from background threads.

    import visionary
    agent = visionary.init()            # from VISIONARY_* / D1_* env vars

Then add the integration for your framework (``visionary.integrations.django``,
``.asgi`` or ``.flask``) and call ``visionary.set_user(id)`` once the user is
known. See README.md and SPEC.md.
"""

from ._agent import Agent, get_agent, init
from ._config import Config, from_env
from ._context import set_route, set_user

__version__ = "0.1.0"

__all__ = ["Agent", "Config", "from_env", "get_agent", "init", "set_route", "set_user", "__version__"]
