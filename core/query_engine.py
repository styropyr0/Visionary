class QueryEngine:
    def __init__(self):
        pass

    def filter_metrics(self, metrics: list, name: str = None) -> list:
        return [m for m in metrics if name is None or m.name == name]

    def group_by_label(self, metrics: list, label_key: str) -> dict:
        groups = {}
        for metric in metrics:
            key = metric.labels.get(label_key, "__unknown__")
            groups.setdefault(key, []).append(metric)
        return groups
