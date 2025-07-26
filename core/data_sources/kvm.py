from base import BaseCollector
from core.models import Metric
import libvirt


class KVMCollector(BaseCollector):
    def collect(self) -> list:
        conn = libvirt.open(self.config.get("uri", "qemu:///system"))
        domains = conn.listAllDomains()
        metrics = []
        for d in domains:
            stats = d.info()
            metrics.append(
                Metric(name="kvm.vm.memory_kb", value=stats[1], labels={"vm": d.name()})
            )
        conn.close()
        return metrics
