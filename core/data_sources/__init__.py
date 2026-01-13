"""Data source collectors for various platforms and services

Collectors are lazily loaded when needed through the registry.
This allows optional dependencies to be missing without breaking imports.
"""

__all__ = [
    'DockerCollector',
    'KubernetesCollector',
    'PrometheusCollector',
    'PostgresCollector',
    'MySQLCollector',
    'RedisCollector',
    'ElasticsearchCollector',
    'InfluxDBCollector',
    'SystemCollector',
    'VMwareCollector',
    'HyperVCollector',
    'KVMCollector',
    'XenCollector',
    'OpenStackCollector',
    'ProxmoxCollector',
]

