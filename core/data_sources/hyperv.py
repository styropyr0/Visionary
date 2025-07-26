from base import BaseCollector
from core.models import Metric
import wmi


class HyperVCollector(BaseCollector):
    def collect(self) -> list:
        c = wmi.WMI()
        metrics = []
        for vm in c.Msvm_ComputerSystem(
            ElementName="Microsoft Hyper-V Manager"
        ).instances():
            metrics.append(
                Metric(
                    name="hyperv.vm.state",
                    value=1 if vm.EnabledState == 2 else 0,
                    labels={"vm": vm.ElementName},
                ),
                Metric(
                    name="hyperv.vm.cpu_count",
                    value=vm.VirtualQuantity,
                    labels={"vm": vm.ElementName},
                ),
                Metric(
                    name="hyperv.vm.memory_mb",
                    value=vm.MemorySize / 1024,
                    labels={"vm": vm.ElementName},
                ),
                Metric(
                    name="hyperv.vm.disk_size_gb",
                    value=vm.HardDrives[0].Capacity / (1024**3),
                    labels={"vm": vm.ElementName},
                ),
                Metric(
                    name="hyperv.vm.network_adapters",
                    value=len(vm.NetworkAdapters),
                    labels={"vm": vm.ElementName},
                ),
            )
        return metrics
