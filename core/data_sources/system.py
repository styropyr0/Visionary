from base import BaseCollector
from core.models import Metric
import psutil


class SystemCollector(BaseCollector):
    def collect(self) -> list:
        return [
            Metric(name="system.cpu_usage", value=psutil.cpu_percent()),
            Metric(name="system.memory_usage", value=psutil.virtual_memory().percent),
            Metric(name="system.disk_usage", value=psutil.disk_usage("/").percent),
        ]
