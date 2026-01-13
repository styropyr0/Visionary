"""
Django Management Command Example
Shows how to integrate Visionary collectors with Django

Usage:
    python manage.py collect_metrics --source system
    python manage.py collect_metrics --source system --aggregation avg
"""

from django.core.management.base import BaseCommand
from core.registry import get_collector, list_collectors
from core.query_engine import QueryEngine


class Command(BaseCommand):
    help = 'Collect and display metrics from Visionary data sources'

    def add_arguments(self, parser):
        parser.add_argument(
            '--source',
            type=str,
            default='system',
            help='Data source to collect from'
        )
        parser.add_argument(
            '--pattern',
            type=str,
            help='Metric name pattern (regex)'
        )
        parser.add_argument(
            '--aggregation',
            type=str,
            choices=['sum', 'avg', 'min', 'max', 'median', 'stddev', 'count', 'p50', 'p95', 'p99'],
            help='Aggregation function'
        )
        parser.add_argument(
            '--limit',
            type=int,
            help='Limit number of results'
        )
        parser.add_argument(
            '--list',
            action='store_true',
            help='List all available data sources'
        )

    def handle(self, *args, **options):
        if options['list']:
            sources = list_collectors()
            self.stdout.write(self.style.SUCCESS('Available data sources:'))
            for source in sources:
                self.stdout.write(f'  - {source}')
            return

        source_name = options['source']
        pattern = options['pattern']
        aggregation = options['aggregation']
        limit = options['limit']

        try:
            # Get collector
            collector_cls = get_collector(source_name)
            if not collector_cls:
                self.stdout.write(self.style.ERROR(f'Data source "{source_name}" not found'))
                return

            # Collect metrics
            self.stdout.write(f'Collecting metrics from {source_name}...')
            collector = collector_cls({})
            metrics = collector.collect()

            # Initialize query engine
            engine = QueryEngine()

            # Apply filtering
            if pattern:
                filtered = engine.filter_metrics(metrics, name_pattern=pattern)
                self.stdout.write(f'Filtered to {len(filtered)} metrics matching "{pattern}"')
            else:
                filtered = metrics
                self.stdout.write(f'Found {len(filtered)} total metrics')

            # Apply aggregation
            if aggregation:
                result = engine.aggregate(filtered, func=aggregation)
                self.stdout.write(self.style.SUCCESS(f'\n{aggregation.upper()}: {result}'))
            else:
                # Apply limit and display
                if limit:
                    filtered = engine.sort_by_value(filtered, ascending=False)[:limit]
                    self.stdout.write(f'Showing top {limit} metrics:')
                else:
                    self.stdout.write('\nMetrics:')

                # Display results
                self.stdout.write(f'\n{"Name":<50} {"Value":<15} {"Labels"}')
                self.stdout.write('-' * 100)
                for metric in filtered[:20]:  # Show max 20
                    labels_str = str(metric.labels) if metric.labels else 'None'
                    self.stdout.write(
                        f'{metric.name:<50} {str(metric.value):<15} {labels_str}'
                    )

        except Exception as e:
            self.stdout.write(self.style.ERROR(f'Error: {str(e)}'))
