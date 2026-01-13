from core.base import BaseCollector
from core.models import Metric
import redis


class RedisCollector(BaseCollector):
    """Redis cache metrics collector"""

    def collect(self) -> list:
        metrics = []
        try:
            r = redis.Redis(**self.config)
            info = r.info()

            # Connection metrics
            metrics.append(
                Metric(name="redis.connected_clients", value=info.get("connected_clients", 0))
            )
            metrics.append(
                Metric(name="redis.blocked_clients", value=info.get("blocked_clients", 0))
            )

            # Memory metrics
            used_memory = info.get("used_memory", 0)
            metrics.append(
                Metric(name="redis.used_memory_bytes", value=used_memory)
            )
            metrics.append(
                Metric(name="redis.used_memory_mb", value=used_memory / 1024 / 1024)
            )
            metrics.append(
                Metric(name="redis.used_memory_peak_bytes", value=info.get("used_memory_peak", 0))
            )

            # Keys metrics
            metrics.append(
                Metric(name="redis.db_keys", value=info.get("db0", {}).get("keys", 0))
            )

            # Command metrics
            metrics.append(
                Metric(name="redis.total_commands_processed", value=info.get("total_commands_processed", 0))
            )
            metrics.append(
                Metric(name="redis.instantaneous_ops_per_sec", value=info.get("instantaneous_ops_per_sec", 0))
            )

            # Hit/Miss ratio
            hits = info.get("keyspace_hits", 0)
            misses = info.get("keyspace_misses", 0)
            total = hits + misses
            hit_ratio = (hits / total * 100) if total > 0 else 0
            metrics.append(
                Metric(name="redis.keyspace_hits", value=hits)
            )
            metrics.append(
                Metric(name="redis.keyspace_misses", value=misses)
            )
            metrics.append(
                Metric(name="redis.hit_ratio_percent", value=hit_ratio)
            )

            # Eviction metrics
            metrics.append(
                Metric(name="redis.evicted_keys", value=info.get("evicted_keys", 0))
            )

            # Replication metrics
            metrics.append(
                Metric(name="redis.connected_slaves", value=info.get("connected_slaves", 0))
            )
            metrics.append(
                Metric(name="redis.role", value=1 if info.get("role") == "master" else 0, labels={"role": info.get("role", "unknown")})
            )

            # Persistence metrics
            metrics.append(
                Metric(name="redis.rdb_last_save_time", value=info.get("rdb_last_save_time", 0))
            )
            metrics.append(
                Metric(name="redis.rdb_changes_since_last_save", value=info.get("rdb_changes_since_last_save", 0))
            )

        except Exception as e:
            metrics.append(Metric(name="redis.error", value=1, labels={"message": str(e)}))

        return metrics
