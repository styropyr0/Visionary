from core.base import BaseCollector
from core.models import Metric
from elasticsearch import Elasticsearch


class ElasticsearchCollector(BaseCollector):
    """Elasticsearch cluster metrics collector"""

    def collect(self) -> list:
        metrics = []
        try:
            es = Elasticsearch(**self.config)
            stats = es.cluster.stats()

            # Node metrics
            nodes_info = stats.get("nodes", {}).get("count", {})
            metrics.append(Metric(name="elasticsearch.nodes.total", value=nodes_info.get("total", 0)))
            metrics.append(Metric(name="elasticsearch.nodes.data", value=nodes_info.get("data", 0)))
            metrics.append(Metric(name="elasticsearch.nodes.master", value=nodes_info.get("master", 0)))

            # Index metrics
            indices_info = stats.get("indices", {})
            metrics.append(Metric(name="elasticsearch.indices.total", value=indices_info.get("count", 0)))
            metrics.append(Metric(name="elasticsearch.indices.shards.total", value=indices_info.get("shards", {}).get("total", 0)))
            metrics.append(Metric(name="elasticsearch.indices.docs.count", value=indices_info.get("docs", {}).get("count", 0)))
            metrics.append(Metric(name="elasticsearch.indices.docs.deleted", value=indices_info.get("docs", {}).get("deleted", 0)))

            # Storage metrics
            store_size = indices_info.get("store", {}).get("size_in_bytes", 0)
            metrics.append(Metric(name="elasticsearch.indices.store_size_bytes", value=store_size))

            # JVM metrics
            jvm_info = stats.get("jvm", {})
            metrics.append(Metric(name="elasticsearch.jvm.heap_used_bytes", value=jvm_info.get("mem", {}).get("heap_used_in_bytes", 0)))
            metrics.append(Metric(name="elasticsearch.jvm.heap_max_bytes", value=jvm_info.get("mem", {}).get("heap_max_in_bytes", 0)))

            # Cluster health
            health = es.cluster.health()
            metrics.append(Metric(name="elasticsearch.cluster.active_shards", value=health.get("active_shards", 0)))
            metrics.append(Metric(name="elasticsearch.cluster.relocating_shards", value=health.get("relocating_shards", 0)))
            metrics.append(Metric(name="elasticsearch.cluster.initializing_shards", value=health.get("initializing_shards", 0)))
            metrics.append(Metric(name="elasticsearch.cluster.unassigned_shards", value=health.get("unassigned_shards", 0)))
            metrics.append(Metric(name="elasticsearch.cluster.status", value=1 if health.get("status") == "green" else 0, labels={"status": health.get("status", "unknown")}))

        except Exception as e:
            metrics.append(Metric(name="elasticsearch.error", value=1, labels={"message": str(e)}))

        return metrics
