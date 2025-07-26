class Metric:
    def __init__(
        self, name: str, value: float, labels: dict = None, timestamp: float = None
    ):
        self.name = name
        self.value = value
        self.labels = labels or {}
        self.timestamp = timestamp

    def to_dict(self):
        return {
            "name": self.name,
            "value": self.value,
            "labels": self.labels,
            "timestamp": self.timestamp,
        }


class Panel:
    def __init__(self, title: str, metrics: list):
        self.title = title
        self.metrics = metrics

    def to_dict(self):
        return {
            "title": self.title,
            "metrics": [m.to_dict() for m in self.metrics],
        }


class Dashboard:
    def __init__(self, name: str, panels: list):
        self.name = name
        self.panels = panels

    def to_dict(self):
        return {
            "name": self.name,
            "panels": [p.to_dict() for p in self.panels],
        }
