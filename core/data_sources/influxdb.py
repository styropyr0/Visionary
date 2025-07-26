from base import BaseCollector
from core.models import Metric
from influxdb import InfluxDBClient


class InfluxDBCollector(BaseCollector):
    def collect(self) -> list:
        client = InfluxDBClient(**self.config)
        results = client.query("SHOW MEASUREMENTS")
        metrics = []
        for m in results.get_points():
            metrics.append(
                Metric(name=f"influxdb.measurement.{m.get('name')}", value=0)
            )
        return metrics
