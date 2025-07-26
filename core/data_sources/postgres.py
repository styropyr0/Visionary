from base import BaseCollector
from core.models import Metric
import psycopg2


class PostgresCollector(BaseCollector):
    def collect(self) -> list:
        conn = psycopg2.connect(**self.config)
        cur = conn.cursor()
        cur.execute("SELECT count(*) FROM pg_stat_activity;")
        active_connections = cur.fetchone()[0]
        cur.execute(
            "SELECT sum(blks_hit) / nullif(sum(blks_hit + blks_read), 0) FROM pg_stat_database;"
        )
        cache_hit_ratio = float(cur.fetchone()[0] or 0)
        cur.close()
        conn.close()
        return [
            Metric(name="postgres.active_connections", value=active_connections),
            Metric(name="postgres.cache_hit_ratio", value=cache_hit_ratio),
        ]
