from base import BaseCollector
from core.models import Metric
import pymysql


class MySQLCollector(BaseCollector):
    def collect(self) -> list:
        conn = pymysql.connect(**self.config)
        cur = conn.cursor()
        cur.execute("SHOW STATUS LIKE 'Threads_connected';")
        active_connections = int(cur.fetchone()[1])
        cur.execute("SHOW STATUS LIKE 'Qcache_hits';")
        cache_hits = int(cur.fetchone()[1])
        cur.close()
        conn.close()
        return [
            Metric(name="mysql.active_connections", value=active_connections),
            Metric(name="mysql.cache_hits", value=cache_hits),
        ]
