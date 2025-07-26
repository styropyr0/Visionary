from base import BaseCollector
from core.models import Metric
import redis


class RedisCollector(BaseCollector):
    def collect(self) -> list:
        r = redis.Redis(**self.config)
        info = r.info()
        return [
            Metric(name="redis.connected_clients", value=info.get("connected_clients")),
            Metric(
                name="redis.used_memory_mb", value=info.get("used_memory") / 1024 / 1024
            ),
        ]
