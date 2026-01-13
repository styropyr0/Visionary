"""Collector registry for dynamic registration and lazy loading"""

_registry = {}
_collectors_to_load = {
    "postgres": ("core.data_sources.postgres", "PostgresCollector"),
    "prometheus": ("core.data_sources.prometheus", "PrometheusCollector"),
    "system": ("core.data_sources.system", "SystemCollector"),
    "docker": ("core.data_sources.docker", "DockerCollector"),
    "kubernetes": ("core.data_sources.kubernetes", "KubernetesCollector"),
    "mysql": ("core.data_sources.mysql", "MySQLCollector"),
    "redis": ("core.data_sources.redis", "RedisCollector"),
    "elasticsearch": ("core.data_sources.elastic_search", "ElasticsearchCollector"),
    "influxdb": ("core.data_sources.influxdb", "InfluxDBCollector"),
    "vmware": ("core.data_sources.vmware", "VMwareCollector"),
    "hyperv": ("core.data_sources.hyperv", "HyperVCollector"),
    "kvm": ("core.data_sources.kvm", "KVMCollector"),
    "xen": ("core.data_sources.xen", "XenCollector"),
    "openstack": ("core.data_sources.openstack", "OpenStackCollector"),
    "proxmox": ("core.data_sources.proxmox", "ProxmoxCollector"),
}


def _lazy_load_collector(source: str):
    """Lazily load a collector class when first requested"""
    if source not in _collectors_to_load:
        return None

    module_path, class_name = _collectors_to_load[source]
    try:
        module = __import__(module_path, fromlist=[class_name])
        collector_cls = getattr(module, class_name)
        _registry[source] = collector_cls
        return collector_cls
    except (ImportError, AttributeError) as e:
        print(f"Warning: Could not load collector '{source}': {e}")
        return None


def register_collector(source: str, collector_cls):
    """Register a custom collector"""
    _registry[source] = collector_cls


def get_collector(source: str):
    """Get a collector by source name (with lazy loading)"""
    if source in _registry:
        return _registry[source]
    return _lazy_load_collector(source)


def list_collectors():
    """List all available collectors (both loaded and loadable)"""
    available = set(_registry.keys()) | set(_collectors_to_load.keys())
    return sorted(available)

