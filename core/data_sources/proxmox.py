from base import BaseCollector
from core.models import Metric
import proxmoxer


class ProxmoxCollector(BaseCollector):
    def collect(self) -> list:
        proxmox = proxmoxer.ProxmoxAPI(**self.config)
        metrics = []
        for node in proxmox.nodes.get():
            metrics.append(
                Metric(
                    name="proxmox.node.cpu",
                    value=node.get("cpu"),
                    labels={"node": node.get("node")},
                )
            )
        return metrics
