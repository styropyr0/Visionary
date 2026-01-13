#!/usr/bin/env python
"""Test script for Visionary core functionality"""

from core.registry import get_collector
from core.query_engine import QueryEngine

def test_system_collector():
    """Test system collector"""
    print("\n=== Testing System Collector ===")
    SystemCollector = get_collector('system')
    collector = SystemCollector({})
    metrics = collector.collect()
    print(f"[OK] Collected {len(metrics)} system metrics")
    for m in metrics:
        print(f"   - {m.name}: {m.value}")
    return metrics

def test_query_engine(metrics):
    """Test advanced query engine"""
    print("\n=== Testing Advanced Query Engine ===")
    engine = QueryEngine()
    
    # Test basic filtering
    filtered = engine.filter_metrics(metrics, name='system.cpu_usage')
    print(f"[OK] filter_metrics: Found {len(filtered)} metrics matching 'system.cpu_usage'")
    
    # Test group_by_label
    grouped = engine.group_by_label(metrics, 'device')
    print(f"[OK] group_by_label: Grouped into {len(grouped)} groups")
    
    # Test aggregation
    avg_value = engine.aggregate(metrics, func='avg')
    print(f"[OK] aggregate (avg): {avg_value:.2f}")
    
    max_value = engine.aggregate(metrics, func='max')
    print(f"[OK] aggregate (max): {max_value}")
    
    min_value = engine.aggregate(metrics, func='min')
    print(f"[OK] aggregate (min): {min_value}")
    
    count = engine.aggregate(metrics, func='count')
    print(f"[OK] aggregate (count): {count}")
    
    # Test sorting
    sorted_metrics = engine.sort_by_value(metrics, ascending=False)
    print(f"[OK] sort_by_value: Sorted {len(sorted_metrics)} metrics")
    print(f"     Top metric: {sorted_metrics[0].name} = {sorted_metrics[0].value}")
    
    # Test top_n
    top_2 = engine.top_n(metrics, 2)
    print(f"[OK] top_n(2): {[m.name for m in top_2]}")
    
    # Test aggregate_by_groups
    by_label = engine.aggregate_by_groups(metrics, group_by='device', func='sum')
    print(f"[OK] aggregate_by_groups: {by_label}")
    
    # Test threshold
    thresholds = engine.apply_threshold(metrics, lower=50, upper=80)
    print(f"[OK] apply_threshold (50-80): ok={len(thresholds['ok'])}, critical={len(thresholds['critical'])}")

def test_response_models():
    """Test response models"""
    print("\n=== Testing Response Models ===")
    from core.response.success_response import SuccessResponse
    
    success = SuccessResponse(code=200, message="Test successful", data={"test": "data"})
    print(f"[OK] SuccessResponse created: {success.message}")
    
    print("\n[SUCCESS] All tests passed!")

if __name__ == "__main__":
    metrics = test_system_collector()
    test_query_engine(metrics)
    test_response_models()


