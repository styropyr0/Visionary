from base import BaseCollector
from core.models import Metric
from openstack import connection


class OpenStackCollector(BaseCollector):
    def collect(self) -> list:
        conn = connection.Connection(**self.config)
        metrics = []
        for server in conn.compute.servers():
            metrics.append(
                Metric(
                    name="openstack.instance.status",
                    value=1 if server.status == "ACTIVE" else 0,
                    labels={"instance": server.name},
                )
            )
        return metrics
