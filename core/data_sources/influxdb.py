from core.base import BaseCollector
from core.models import Metric
from influxdb import InfluxDBClient


class InfluxDBCollector(BaseCollector):
    """InfluxDB time-series database metrics collector"""

    def collect(self) -> list:
        metrics = []
        try:
            client = InfluxDBClient(**self.config)

            # Database metrics
            results = client.get_list_database()
            metrics.append(Metric(name="influxdb.databases.count", value=len(results)))

            # Measurements
            measurements = client.query("SHOW MEASUREMENTS")
            measurement_count = sum(1 for _ in measurements.get_points())
            metrics.append(Metric(name="influxdb.measurements.count", value=measurement_count))

            # Retention policies
            rps = client.get_list_retention_policies()
            metrics.append(Metric(name="influxdb.retention_policies.count", value=len(rps)))

            # Series count
            series = client.query("SHOW SERIES")
            series_count = sum(1 for _ in series.get_points())
            metrics.append(Metric(name="influxdb.series.count", value=series_count))

            # Users
            users = client.get_list_users()
            metrics.append(Metric(name="influxdb.users.count", value=len(users)))

            # Write operations
            write_stats = client.query("SHOW STATS")
            for point in write_stats.get_points():
                if point.get("write") is not None:
                    metrics.append(
                        Metric(name="influxdb.write_operations", value=float(point.get("write", 0)))
                    )
                if point.get("query") is not None:
                    metrics.append(
                        Metric(name="influxdb.query_operations", value=float(point.get("query", 0)))
                    )

            client.close()

        except Exception as e:
            metrics.append(Metric(name="influxdb.error", value=1, labels={"message": str(e)}))

        return metrics
