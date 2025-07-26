from core.data_sources.postgres import PostgresCollector
from core.data_sources.prometheus import PrometheusCollector
from core.data_sources.system import SystemCollector

_registry = {
    "postgres": PostgresCollector,
    "prometheus": PrometheusCollector,
    "system": SystemCollector,
}


def register_collector(source: str, collector_cls):
    _registry[source] = collector_cls


def get_collector(source: str):
    return _registry.get(source)
