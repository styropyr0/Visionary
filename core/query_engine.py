"""Advanced query engine for metrics filtering, grouping, and aggregation"""

from typing import List, Dict, Optional, Union
from datetime import datetime, timedelta
from statistics import mean, median, stdev, quantiles
import re


class QueryEngine:
    """
    Advanced query engine supporting:
    - Metric filtering by name, labels, and regex patterns
    - Grouping by label keys
    - Aggregation functions (sum, avg, min, max, percentile, stddev, count)
    - Time-range filtering
    - Label value filtering
    """

    def __init__(self):
        pass

    def filter_metrics(
        self,
        metrics: list,
        name: str = None,
        name_pattern: str = None,
        labels: Dict[str, str] = None,
        timestamp_from: float = None,
        timestamp_to: float = None,
    ) -> list:
        """
        Filter metrics by name, regex pattern, labels, or time range.

        Args:
            metrics: List of Metric objects
            name: Exact metric name match
            name_pattern: Regex pattern for metric name
            labels: Dict of label key-value pairs to match
            timestamp_from: Filter metrics after this timestamp (seconds since epoch)
            timestamp_to: Filter metrics before this timestamp

        Returns:
            Filtered list of metrics
        """
        result = metrics

        # Filter by exact name
        if name:
            result = [m for m in result if m.name == name]

        # Filter by regex pattern
        if name_pattern:
            pattern = re.compile(name_pattern)
            result = [m for m in result if pattern.search(m.name)]

        # Filter by labels
        if labels:
            for label_key, label_value in labels.items():
                result = [
                    m for m in result
                    if m.labels.get(label_key) == label_value
                ]

        # Filter by time range
        if timestamp_from is not None:
            result = [m for m in result if m.timestamp is None or m.timestamp >= timestamp_from]
        if timestamp_to is not None:
            result = [m for m in result if m.timestamp is None or m.timestamp <= timestamp_to]

        return result

    def group_by_label(self, metrics: list, label_key: str) -> dict:
        """
        Group metrics by a specific label key.

        Args:
            metrics: List of Metric objects
            label_key: Label key to group by

        Returns:
            Dict mapping label values to lists of metrics
        """
        groups = {}
        for metric in metrics:
            key = metric.labels.get(label_key, "__unknown__")
            groups.setdefault(key, []).append(metric)
        return groups

    def group_by_multiple_labels(self, metrics: list, label_keys: List[str]) -> dict:
        """Group metrics by multiple label keys."""
        groups = {}
        for metric in metrics:
            key_tuple = tuple(
                metric.labels.get(k, "__unknown__") for k in label_keys
            )
            key_str = " | ".join(key_tuple)
            groups.setdefault(key_str, []).append(metric)
        return groups

    def aggregate(self, metrics: list, func: str = "avg") -> Union[float, int, None]:
        """
        Aggregate metric values.

        Args:
            metrics: List of Metric objects
            func: Aggregation function - 'sum', 'avg', 'min', 'max', 'median',
                  'stddev', 'count', 'p50', 'p95', 'p99'

        Returns:
            Aggregated value
        """
        if not metrics:
            return None

        values = [m.value for m in metrics if isinstance(m.value, (int, float))]
        if not values:
            return None

        if func == "sum":
            return sum(values)
        elif func == "avg":
            return mean(values)
        elif func == "min":
            return min(values)
        elif func == "max":
            return max(values)
        elif func == "median":
            return median(values)
        elif func == "stddev":
            return stdev(values) if len(values) > 1 else 0
        elif func == "count":
            return len(values)
        elif func.startswith("p"):
            try:
                percentile = int(func[1:]) / 100.0
                if len(values) < 2:
                    return values[0] if values else None
                quantile_list = quantiles(values, n=100)
                index = int(percentile * len(quantile_list))
                return quantile_list[min(index, len(quantile_list) - 1)]
            except (ValueError, IndexError):
                return None
        else:
            raise ValueError(f"Unknown aggregation function: {func}")

    def aggregate_by_groups(
        self,
        metrics: list,
        group_by: str,
        func: str = "avg",
    ) -> dict:
        """
        Group metrics and aggregate each group.

        Args:
            metrics: List of Metric objects
            group_by: Label key to group by
            func: Aggregation function

        Returns:
            Dict mapping label values to aggregated values
        """
        groups = self.group_by_label(metrics, group_by)
        return {
            label: self.aggregate(group_metrics, func)
            for label, group_metrics in groups.items()
        }

    def sort_by_value(
        self, metrics: list, ascending: bool = True
    ) -> list:
        """Sort metrics by their values."""
        return sorted(metrics, key=lambda m: m.value, reverse=not ascending)

    def limit(self, metrics: list, limit: int) -> list:
        """Limit the number of returned metrics."""
        return metrics[:limit]

    def top_n(self, metrics: list, n: int) -> list:
        """Get top N metrics by value."""
        return self.sort_by_value(metrics, ascending=False)[:n]

    def bottom_n(self, metrics: list, n: int) -> list:
        """Get bottom N metrics by value."""
        return self.sort_by_value(metrics, ascending=True)[:n]

    def rate_of_change(
        self, current_metrics: list, previous_metrics: list
    ) -> list:
        """
        Calculate rate of change between two metric sets.
        Useful for monitoring deltas.
        """
        result = []
        current_dict = {m.name: m.value for m in current_metrics}
        previous_dict = {m.name: m.value for m in previous_metrics}

        for name, current_value in current_dict.items():
            if name in previous_dict:
                previous_value = previous_dict[name]
                if previous_value != 0:
                    change_rate = (current_value - previous_value) / previous_value * 100
                    metric = current_metrics[0].__class__(
                        name=f"{name}.delta",
                        value=change_rate,
                        labels={"metric": name},
                    )
                    result.append(metric)

        return result

    def apply_threshold(
        self, metrics: list, lower: float = None, upper: float = None
    ) -> dict:
        """
        Apply threshold checking to metrics.

        Returns dict with 'ok', 'warning', 'critical' keys.
        """
        categorized = {"ok": [], "warning": [], "critical": []}

        for metric in metrics:
            status = "ok"
            if lower is not None and metric.value < lower:
                status = "critical"
            elif upper is not None and metric.value > upper:
                status = "critical"
            categorized[status].append(metric)

        return categorized

