from core.base import BaseCollector
from core.models import Metric
import psycopg2


class PostgresCollector(BaseCollector):
    """PostgreSQL database metrics collector"""

    def collect(self) -> list:
        metrics = []
        try:
            conn = psycopg2.connect(**self.config)
            cur = conn.cursor()

            # Connection metrics
            cur.execute("SELECT count(*) FROM pg_stat_activity;")
            active_connections = cur.fetchone()[0]
            metrics.append(Metric(name="postgres.active_connections", value=active_connections))

            # Cache hit ratio
            cur.execute(
                "SELECT sum(blks_hit) / nullif(sum(blks_hit + blks_read), 0) FROM pg_stat_database;"
            )
            cache_hit_ratio = float(cur.fetchone()[0] or 0)
            metrics.append(Metric(name="postgres.cache_hit_ratio", value=cache_hit_ratio))

            # Database size
            cur.execute(
                "SELECT datname, pg_database_size(datname) FROM pg_database WHERE datistemplate = false;"
            )
            for db_name, db_size in cur.fetchall():
                metrics.append(
                    Metric(name="postgres.database_size_bytes", value=db_size, labels={"database": db_name})
                )

            # Transaction metrics
            cur.execute("SELECT sum(xact_commit) FROM pg_stat_database;")
            xact_commit = cur.fetchone()[0] or 0
            metrics.append(Metric(name="postgres.transactions_committed", value=xact_commit))

            cur.execute("SELECT sum(xact_rollback) FROM pg_stat_database;")
            xact_rollback = cur.fetchone()[0] or 0
            metrics.append(Metric(name="postgres.transactions_rollback", value=xact_rollback))

            # Tuple metrics
            cur.execute("SELECT sum(tup_returned) FROM pg_stat_database;")
            tup_returned = cur.fetchone()[0] or 0
            metrics.append(Metric(name="postgres.tuples_returned", value=tup_returned))

            cur.execute("SELECT sum(tup_inserted) FROM pg_stat_database;")
            tup_inserted = cur.fetchone()[0] or 0
            metrics.append(Metric(name="postgres.tuples_inserted", value=tup_inserted))

            cur.close()
            conn.close()

        except Exception as e:
            metrics.append(Metric(name="postgres.error", value=1, labels={"message": str(e)}))

        return metrics
