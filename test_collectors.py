#!/usr/bin/env python
"""
Test script for all enhanced data source collectors
Tests each collector's ability to collect metrics
"""

from core.registry import get_collector
from core.query_engine import QueryEngine

def test_collector(source_name, config=None):
    """Test a single collector"""
    config = config or {}
    try:
        CollectorClass = get_collector(source_name)
        if not CollectorClass:
            return f"[SKIP] {source_name}: Not found in registry"
        
        collector = CollectorClass(config)
        metrics = collector.collect()
        
        if not metrics:
            return f"[WARN] {source_name}: Returned 0 metrics"
        
        # Count unique metric names
        unique_names = set(m.name for m in metrics)
        
        result = f"[OK] {source_name}: {len(metrics)} metrics ({len(unique_names)} unique)"
        result += f"\n      Sample metrics:"
        for m in list(metrics)[:3]:
            labels_str = f" {m.labels}" if m.labels else ""
            result += f"\n        - {m.name}: {m.value}{labels_str}"
        
        return result
        
    except ImportError as e:
        return f"[SKIP] {source_name}: Missing dependency - {str(e).split('No module')[1] if 'No module' in str(e) else str(e)}"
    except Exception as e:
        return f"[ERROR] {source_name}: {str(e)[:80]}"


def test_query_engine():
    """Test query engine with sample metrics"""
    print("\n" + "="*70)
    print("TESTING QUERY ENGINE")
    print("="*70)
    
    try:
        # Get system metrics
        SystemCollector = get_collector('system')
        collector = SystemCollector({})
        metrics = collector.collect()
        
        engine = QueryEngine()
        
        # Test filtering
        cpu_metrics = engine.filter_metrics(metrics, name='system.cpu_usage')
        print(f"[OK] Filter by name: Found {len(cpu_metrics)} metrics")
        
        # Test pattern matching
        memory_metrics = engine.filter_metrics(metrics, name_pattern='system.memory.*')
        print(f"[OK] Filter by regex: Found {len(memory_metrics)} metrics")
        
        # Test aggregation
        avg_val = engine.aggregate(metrics, 'avg')
        print(f"[OK] Aggregate (avg): {avg_val:.2f}")
        
        max_val = engine.aggregate(metrics, 'max')
        print(f"[OK] Aggregate (max): {max_val}")
        
        # Test sorting
        sorted_metrics = engine.sort_by_value(metrics, ascending=False)
        top_metric = sorted_metrics[0] if sorted_metrics else None
        print(f"[OK] Sort by value: Top metric is {top_metric.name if top_metric else 'N/A'}")
        
        # Test top_n
        top_5 = engine.top_n(metrics, 5)
        print(f"[OK] Top N (5): Found {len(top_5)} metrics")
        
        # Test grouping
        groups = engine.group_by_label(metrics, 'core')
        print(f"[OK] Group by label: {len(groups)} groups")
        
        # Test aggregate_by_groups
        by_group = engine.aggregate_by_groups(metrics, 'core', 'avg')
        print(f"[OK] Aggregate by groups: {len(by_group)} groups with aggregates")
        
    except Exception as e:
        print(f"[ERROR] Query engine: {str(e)}")


def main():
    print("\n" + "="*70)
    print("VISIONARY COLLECTOR TEST SUITE")
    print("="*70 + "\n")
    
    # Enhanced collectors with expected configurations
    collectors_to_test = [
        ("system", {}),
        ("postgres", {"host": "localhost", "user": "postgres", "password": "password"}),
        ("mysql", {"host": "localhost", "user": "root", "password": "password"}),
        ("redis", {"host": "localhost", "port": 6379}),
        ("kubernetes", {"kubeconfig": None}),
        ("prometheus", {"url": "http://localhost:9090"}),
        ("elasticsearch", {"hosts": ["localhost"]}),
        ("influxdb", {"host": "localhost", "port": 8086}),
    ]
    
    print("Testing Enhanced Collectors:\n")
    
    results = []
    for source, config in collectors_to_test:
        result = test_collector(source, config)
        results.append(result)
        print(result)
        print()
    
    # Test query engine
    test_query_engine()
    
    # Summary
    print("\n" + "="*70)
    print("TEST SUMMARY")
    print("="*70)
    
    ok_count = sum(1 for r in results if r.startswith("[OK]"))
    error_count = sum(1 for r in results if r.startswith("[ERROR]"))
    skip_count = sum(1 for r in results if r.startswith("[SKIP]"))
    warn_count = sum(1 for r in results if r.startswith("[WARN]"))
    
    print(f"\nResults:")
    print(f"  [OK]    : {ok_count} collectors")
    print(f"  [ERROR] : {error_count} collectors")
    print(f"  [SKIP]  : {skip_count} collectors (missing dependencies)")
    print(f"  [WARN]  : {warn_count} collectors")
    print(f"\nNote: [SKIP] results are expected if dependencies are not installed")
    print(f"      or if services (PostgreSQL, MySQL, etc.) are not running.")
    
    print("\n" + "="*70)
    if ok_count > 0:
        print(f"SUCCESS: {ok_count}/{len(collectors_to_test)} collectors working\n")
    else:
        print("No collectors available - install dependencies or run local services\n")


if __name__ == "__main__":
    main()
