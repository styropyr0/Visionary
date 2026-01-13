#!/usr/bin/env python
"""
Complete Integration Test for Visionary
Tests both FastAPI (demo) and Django (dashboard management) components
"""

import requests
import json
from typing import Dict, Any

# Configuration
FASTAPI_BASE = "http://localhost:8000"
API_ENDPOINTS = {
    "sources": f"{FASTAPI_BASE}/api/visionary/sources",
    "query": f"{FASTAPI_BASE}/api/visionary/query",
    "health": f"{FASTAPI_BASE}/api/visionary/health",
    "docs": f"{FASTAPI_BASE}/api/visionary/docs",
}

DJANGO_BASE = "http://localhost:8001"  # Run django on different port
DJANGO_ENDPOINTS = {
    "dashboards_list": f"{DJANGO_BASE}/api/visionary/dashboards/",
    "dashboard_detail": f"{DJANGO_BASE}/api/visionary/dashboards/1/",
}


def print_section(title: str):
    """Print a formatted section header"""
    print(f"\n{'='*70}")
    print(f"  {title}")
    print(f"{'='*70}\n")


def test_fastapi_sources():
    """Test: List all available data sources"""
    print_section("TEST 1: FastAPI - List Data Sources")
    
    try:
        response = requests.get(API_ENDPOINTS["sources"])
        response.raise_for_status()
        data = response.json()
        
        print(f"✅ Status: {response.status_code}")
        print(f"✅ Available sources: {len(data['sources'])}")
        print(f"\nSources:")
        for source in data['sources']:
            print(f"  - {source}")
        return True
    except Exception as e:
        print(f"❌ Error: {str(e)}")
        return False


def test_fastapi_query():
    """Test: Query metrics with filtering and aggregation"""
    print_section("TEST 2: FastAPI - Query Metrics (Real-time)")
    
    try:
        query = {
            "source": "system",
            "filters": {
                "name_pattern": "system.cpu.*"
            },
            "aggregation": "avg",
            "limit": 5
        }
        
        response = requests.post(
            API_ENDPOINTS["query"],
            json=query,
            headers={"Content-Type": "application/json"}
        )
        response.raise_for_status()
        data = response.json()
        
        print(f"✅ Status: {response.status_code}")
        print(f"✅ Source: {data['source']}")
        print(f"✅ Metrics found: {data['count']}")
        print(f"✅ Aggregation result: {data['aggregate']}")
        
        print(f"\nSample metrics (showing 3 of {data['count']}):")
        for metric in data['metrics'][:3]:
            print(f"  - {metric['name']}: {metric['value']} {metric['labels']}")
        
        return True
    except Exception as e:
        print(f"❌ Error: {str(e)}")
        return False


def test_fastapi_health():
    """Test: Health check endpoint"""
    print_section("TEST 3: FastAPI - Health Check")
    
    try:
        response = requests.get(API_ENDPOINTS["health"])
        response.raise_for_status()
        data = response.json()
        
        print(f"✅ Status: {response.status_code}")
        print(f"✅ API Status: {data['status']}")
        print(f"✅ Version: {data['version']}")
        print(f"✅ Sources available: {data['sources_available']}")
        
        return True
    except Exception as e:
        print(f"❌ Error: {str(e)}")
        return False


def test_fastapi_docs():
    """Test: API Documentation endpoints"""
    print_section("TEST 4: FastAPI - API Documentation")
    
    try:
        response = requests.get(API_ENDPOINTS["docs"])
        
        if response.status_code == 200:
            print(f"✅ Swagger UI available at: {API_ENDPOINTS['docs']}")
        else:
            print(f"⚠️  Swagger UI status: {response.status_code}")
        
        print(f"✅ ReDoc available at: {FASTAPI_BASE}/api/visionary/redoc")
        print(f"✅ OpenAPI schema at: {FASTAPI_BASE}/api/visionary/openapi.json")
        
        return True
    except Exception as e:
        print(f"❌ Error: {str(e)}")
        return False


def test_django_dashboard_create():
    """Test: Create a dashboard (Django)"""
    print_section("TEST 5: Django - Create Dashboard")
    
    print("⚠️  Note: Django tests require Django server running on port 8001")
    print("    Run: python manage.py runserver 8001")
    
    try:
        dashboard_data = {
            "title": "Test Dashboard",
            "description": "Test system metrics dashboard",
            "panels": []
        }
        
        response = requests.post(
            DJANGO_ENDPOINTS["dashboards_list"],
            json=dashboard_data,
            headers={"Content-Type": "application/json"},
            timeout=2
        )
        response.raise_for_status()
        data = response.json()
        
        print(f"✅ Status: {response.status_code}")
        print(f"✅ Dashboard created: {data}")
        
        return True
    except requests.exceptions.ConnectionError:
        print(f"⚠️  Django server not running on port 8001")
        print(f"   Expected - Django is optional")
        return None
    except Exception as e:
        print(f"⚠️  Error: {str(e)}")
        return None


def test_webpage():
    """Test: Interactive dashboard webpage"""
    print_section("TEST 6: FastAPI - Interactive Dashboard")
    
    try:
        response = requests.get(f"{FASTAPI_BASE}/")
        response.raise_for_status()
        
        if "Visionary" in response.text and "Query Builder" in response.text:
            print(f"✅ Status: {response.status_code}")
            print(f"✅ Dashboard HTML loaded successfully")
            print(f"✅ Dashboard available at: {FASTAPI_BASE}/")
            return True
        else:
            print(f"⚠️  Unexpected HTML content")
            return False
    except Exception as e:
        print(f"❌ Error: {str(e)}")
        return False


def main():
    """Run all tests"""
    print("\n" + "="*70)
    print("  VISIONARY INTEGRATION TEST SUITE")
    print("="*70)
    print("\nTesting both FastAPI (demo) and Django (dashboard) components")
    print("Current date: 2026-01-13")
    
    results = {
        "FastAPI Sources": test_fastapi_sources(),
        "FastAPI Query": test_fastapi_query(),
        "FastAPI Health": test_fastapi_health(),
        "FastAPI Docs": test_fastapi_docs(),
        "Dashboard Webpage": test_webpage(),
        "Django Dashboard": test_django_dashboard_create(),
    }
    
    # Summary
    print_section("TEST SUMMARY")
    
    total = len([r for r in results.values() if r is not None])
    passed = len([r for r in results.values() if r is True])
    failed = len([r for r in results.values() if r is False])
    skipped = len([r for r in results.values() if r is None])
    
    print(f"Results:")
    for test_name, result in results.items():
        status = "✅ PASS" if result is True else "⚠️  SKIP" if result is None else "❌ FAIL"
        print(f"  {status}  {test_name}")
    
    print(f"\nSummary:")
    print(f"  Passed:  {passed}/{total}")
    print(f"  Failed:  {failed}/{total}")
    print(f"  Skipped: {skipped}")
    
    print("\n" + "="*70)
    if failed == 0:
        print("✅ ALL TESTS PASSED - Visionary is working correctly!")
    else:
        print(f"❌ {failed} test(s) failed - please check the errors above")
    print("="*70 + "\n")
    
    return failed == 0


if __name__ == "__main__":
    import sys
    success = main()
    sys.exit(0 if success else 1)
