from base import BaseCollector
from core.models import Metric
import requests


class PrometheusCollector(BaseCollector):
    def collect(self) -> list:
        base_url = self.config.get("url", "http://localhost:9090")
        metrics = []

        try:
            duration_resp = requests.get(
                f"{base_url}/api/v1/query", params={"query": "scrape_duration_seconds"}
            )
            duration_data = duration_resp.json()["data"]["result"]
            if duration_data:
                value = float(duration_data[0]["value"][1])
                metrics.append(Metric(name="prometheus.scrape_duration", value=value))

            targets_resp = requests.get(f"{base_url}/api/v1/targets")
            targets_data = targets_resp.json()
            active_targets = len(targets_data.get("data", {}).get("activeTargets", []))
            metrics.append(
                Metric(name="prometheus.active_targets", value=active_targets)
            )

        except Exception as e:
            metrics.append(
                Metric(name="prometheus.error", value=1, labels={"message": str(e)})
            )

        return metrics
