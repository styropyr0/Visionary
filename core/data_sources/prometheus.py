from core.base import BaseCollector
from core.models import Metric
import requests


class PrometheusCollector(BaseCollector):
    """Prometheus metrics server collector"""

    def collect(self) -> list:
        metrics = []
        base_url = self.config.get("url", "http://localhost:9090")

        try:
            # Scrape duration
            duration_resp = requests.get(
                f"{base_url}/api/v1/query", params={"query": "scrape_duration_seconds"}
            )
            if duration_resp.status_code == 200:
                duration_data = duration_resp.json()["data"]["result"]
                if duration_data:
                    value = float(duration_data[0]["value"][1])
                    metrics.append(Metric(name="prometheus.scrape_duration_seconds", value=value))

            # Target metrics
            targets_resp = requests.get(f"{base_url}/api/v1/targets")
            if targets_resp.status_code == 200:
                targets_data = targets_resp.json()
                active_targets = len(targets_data.get("data", {}).get("activeTargets", []))
                dropped_targets = len(targets_data.get("data", {}).get("droppedTargets", []))
                metrics.append(Metric(name="prometheus.active_targets", value=active_targets))
                metrics.append(Metric(name="prometheus.dropped_targets", value=dropped_targets))

            # Alerts
            alerts_resp = requests.get(f"{base_url}/api/v1/alerts")
            if alerts_resp.status_code == 200:
                alerts_data = alerts_resp.json()
                alerts = alerts_data.get("data", {}).get("alerts", [])
                metrics.append(Metric(name="prometheus.alerts_total", value=len(alerts)))

                alert_by_state = {}
                for alert in alerts:
                    state = alert.get("state", "unknown")
                    alert_by_state[state] = alert_by_state.get(state, 0) + 1

                for state, count in alert_by_state.items():
                    metrics.append(
                        Metric(name="prometheus.alerts_by_state", value=count, labels={"state": state})
                    )

            # Query statistics
            query_stats_resp = requests.get(
                f"{base_url}/api/v1/query", params={"query": "prometheus_engine_queries_concurrent"}
            )
            if query_stats_resp.status_code == 200:
                query_data = query_stats_resp.json()["data"]["result"]
                if query_data:
                    value = float(query_data[0]["value"][1])
                    metrics.append(Metric(name="prometheus.concurrent_queries", value=value))

        except Exception as e:
            metrics.append(Metric(name="prometheus.error", value=1, labels={"message": str(e)}))

        return metrics
