from base import BaseCollector
from core.models import Metric
from kubernetes import client, config


class KubernetesCollector(BaseCollector):
    def collect(self) -> list:
        config.load_kube_config()
        v1 = client.CoreV1Api()
        nodes = v1.list_node().items
        return [
            Metric(name="k8s.node.up", value=1, labels={"node": n.metadata.name})
            for n in nodes
        ]
