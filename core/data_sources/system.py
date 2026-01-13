from core.base import BaseCollector
from core.models import Metric
import psutil


class SystemCollector(BaseCollector):
    """System metrics collector for CPU, memory, disk, and network"""

    def collect(self) -> list:
        metrics = []

        # CPU metrics
        metrics.append(Metric(name="system.cpu_usage", value=psutil.cpu_percent(interval=1)))
        metrics.extend([
            Metric(name="system.cpu_usage_per_core", value=cpu, labels={"core": str(i)})
            for i, cpu in enumerate(psutil.cpu_percent(percpu=True))
        ])

        # Memory metrics
        vm = psutil.virtual_memory()
        metrics.extend([
            Metric(name="system.memory_usage", value=vm.percent),
            Metric(name="system.memory_used_bytes", value=vm.used),
            Metric(name="system.memory_available_bytes", value=vm.available),
            Metric(name="system.memory_total_bytes", value=vm.total),
        ])

        # Disk metrics
        for disk_path in ["/", "/tmp"] if hasattr(psutil.disk_partitions, "__call__") else ["/"]:
            try:
                disk = psutil.disk_usage(disk_path)
                metrics.extend([
                    Metric(name="system.disk_usage_percent", value=disk.percent, labels={"path": disk_path}),
                    Metric(name="system.disk_used_bytes", value=disk.used, labels={"path": disk_path}),
                    Metric(name="system.disk_total_bytes", value=disk.total, labels={"path": disk_path}),
                ])
            except:
                pass

        # Network metrics
        try:
            net_io = psutil.net_io_counters()
            metrics.extend([
                Metric(name="system.network_bytes_sent", value=net_io.bytes_sent),
                Metric(name="system.network_bytes_recv", value=net_io.bytes_recv),
                Metric(name="system.network_packets_sent", value=net_io.packets_sent),
                Metric(name="system.network_packets_recv", value=net_io.packets_recv),
                Metric(name="system.network_errors_in", value=net_io.errin),
                Metric(name="system.network_errors_out", value=net_io.errout),
            ])
        except:
            pass

        # Process metrics
        metrics.append(Metric(name="system.process_count", value=len(psutil.pids())))

        # Load average
        try:
            load_avg = psutil.getloadavg()
            metrics.extend([
                Metric(name="system.load_average_1m", value=load_avg[0]),
                Metric(name="system.load_average_5m", value=load_avg[1]),
                Metric(name="system.load_average_15m", value=load_avg[2]),
            ])
        except:
            pass

        return metrics
