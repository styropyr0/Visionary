from core.base import BaseCollector
from core.models import Metric
import pymysql


class MySQLCollector(BaseCollector):
    """MySQL database metrics collector"""

    def collect(self) -> list:
        metrics = []
        try:
            conn = pymysql.connect(**self.config)
            cur = conn.cursor()

            # Connection metrics
            cur.execute("SHOW STATUS LIKE 'Threads_connected';")
            active_connections = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.active_connections", value=active_connections))

            cur.execute("SHOW STATUS LIKE 'Threads_running';")
            running_threads = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.running_threads", value=running_threads))

            # Cache metrics
            cur.execute("SHOW STATUS LIKE 'Qcache_hits';")
            cache_hits = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.cache_hits", value=cache_hits))

            cur.execute("SHOW STATUS LIKE 'Qcache_inserts';")
            cache_inserts = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.cache_inserts", value=cache_inserts))

            # Query metrics
            cur.execute("SHOW STATUS LIKE 'Questions';")
            questions = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.questions", value=questions))

            cur.execute("SHOW STATUS LIKE 'Slow_queries';")
            slow_queries = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.slow_queries", value=slow_queries))

            # InnoDB metrics
            cur.execute("SHOW STATUS LIKE 'Innodb_data_reads';")
            innodb_reads = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.innodb_data_reads", value=innodb_reads))

            cur.execute("SHOW STATUS LIKE 'Innodb_data_writes';")
            innodb_writes = int(cur.fetchone()[1])
            metrics.append(Metric(name="mysql.innodb_data_writes", value=innodb_writes))

            # Database size
            cur.execute("SELECT table_schema, SUM(data_length + index_length) FROM information_schema.TABLES GROUP BY table_schema;")
            for schema, size in cur.fetchall():
                if schema and size:
                    metrics.append(
                        Metric(name="mysql.database_size_bytes", value=size, labels={"schema": schema})
                    )

            cur.close()
            conn.close()

        except Exception as e:
            metrics.append(Metric(name="mysql.error", value=1, labels={"message": str(e)}))

        return metrics
