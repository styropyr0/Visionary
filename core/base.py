class BaseCollector:
    def __init__(self, config: dict):
        self.config = config

    def collect(self) -> list:
        raise NotImplementedError("Collector must implement collect method")