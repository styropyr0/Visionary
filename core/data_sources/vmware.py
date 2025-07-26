from base import BaseCollector
from core.models import Metric
from pyVim.connect import SmartConnect, Disconnect
from pyVmomi import vim
import ssl


class VMwareCollector(BaseCollector):
    def collect(self) -> list:
        context = ssl._create_unverified_context()
        si = SmartConnect(
            host=self.config.get("host"),
            user=self.config.get("username"),
            pwd=self.config.get("password"),
            sslContext=context,
        )
        content = si.RetrieveContent()
        metrics = []
        for dc in content.rootFolder.childEntity:
            for vm in dc.vmFolder.childEntity:
                metrics.append(
                    Metric(
                        name="vmware.vm.cpu_mhz",
                        value=vm.summary.quickStats.overallCpuUsage,
                        labels={"vm": vm.summary.config.name},
                    )
                )
        Disconnect(si)
        return metrics
