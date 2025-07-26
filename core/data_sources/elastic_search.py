from base import BaseCollector
from core.models import Metric
from elasticsearch import Elasticsearch


class ElasticsearchCollector(BaseCollector):
    def collect(self) -> list:
        es = Elasticsearch(**self.config)
        stats = es.cluster.stats()
        nodes = stats.get("nodes", {}).get("count", {}).get("total", 0)
        return [Metric(name="elasticsearch.nodes.total", value=nodes)]
