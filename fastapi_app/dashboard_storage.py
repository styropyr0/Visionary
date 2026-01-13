"""
Dashboard storage and data collection.
Stores dashboard configurations and collected metrics.
"""

import json
import os
from datetime import datetime
from typing import List, Dict, Any, Optional

DATA_DIR = os.path.join(os.path.dirname(__file__), '..', 'data')
DASHBOARDS_FILE = os.path.join(DATA_DIR, 'dashboards.json')
METRICS_FILE = os.path.join(DATA_DIR, 'metrics.json')

def ensure_storage():
    """Initialize data directory and files"""
    os.makedirs(DATA_DIR, exist_ok=True)
    
    if not os.path.exists(DASHBOARDS_FILE):
        with open(DASHBOARDS_FILE, 'w') as f:
            json.dump({"dashboards": []}, f, indent=2)
    
    if not os.path.exists(METRICS_FILE):
        with open(METRICS_FILE, 'w') as f:
            json.dump({"metrics": []}, f, indent=2)

def create_dashboard(title: str, description: str = "", source: str = "", metric_patterns: List[str] = None) -> Dict:
    """Create a new dashboard configuration"""
    ensure_storage()
    
    with open(DASHBOARDS_FILE, 'r') as f:
        data = json.load(f)
    
    dashboard_id = max([d['id'] for d in data['dashboards']], default=0) + 1
    
    dashboard = {
        "id": dashboard_id,
        "title": title,
        "description": description,
        "source": source,
        "metric_patterns": metric_patterns or [],
        "created_at": datetime.now().isoformat(),
        "updated_at": datetime.now().isoformat(),
        "enabled": True
    }
    
    data['dashboards'].append(dashboard)
    
    with open(DASHBOARDS_FILE, 'w') as f:
        json.dump(data, f, indent=2)
    
    return dashboard

def get_dashboard(dashboard_id: int) -> Optional[Dict]:
    """Get a dashboard by ID"""
    ensure_storage()
    
    with open(DASHBOARDS_FILE, 'r') as f:
        data = json.load(f)
    
    for dashboard in data['dashboards']:
        if dashboard['id'] == dashboard_id:
            return dashboard
    
    return None

def list_dashboards(limit: int = 50) -> List[Dict]:
    """List all dashboards"""
    ensure_storage()
    
    with open(DASHBOARDS_FILE, 'r') as f:
        data = json.load(f)
    
    return data['dashboards'][-limit:] if limit else data['dashboards']

def update_dashboard(dashboard_id: int, **kwargs) -> Optional[Dict]:
    """Update a dashboard"""
    ensure_storage()
    
    with open(DASHBOARDS_FILE, 'r') as f:
        data = json.load(f)
    
    for dashboard in data['dashboards']:
        if dashboard['id'] == dashboard_id:
            dashboard.update(kwargs)
            dashboard['updated_at'] = datetime.now().isoformat()
            
            with open(DASHBOARDS_FILE, 'w') as f:
                json.dump(data, f, indent=2)
            
            return dashboard
    
    return None

def delete_dashboard(dashboard_id: int) -> bool:
    """Delete a dashboard"""
    ensure_storage()
    
    with open(DASHBOARDS_FILE, 'r') as f:
        data = json.load(f)
    
    original_count = len(data['dashboards'])
    data['dashboards'] = [d for d in data['dashboards'] if d['id'] != dashboard_id]
    
    if len(data['dashboards']) < original_count:
        with open(DASHBOARDS_FILE, 'w') as f:
            json.dump(data, f, indent=2)
        return True
    
    return False

def store_metrics(dashboard_id: int, metrics: List[Dict]) -> int:
    """Store collected metrics for a dashboard"""
    ensure_storage()
    
    with open(METRICS_FILE, 'r') as f:
        data = json.load(f)
    
    for metric in metrics:
        data['metrics'].append({
            "dashboard_id": dashboard_id,
            "name": metric.get('name'),
            "value": metric.get('value'),
            "labels": metric.get('labels', {}),
            "timestamp": datetime.now().isoformat()
        })
    
    with open(METRICS_FILE, 'w') as f:
        json.dump(data, f, indent=2)
    
    return len(metrics)

def get_dashboard_metrics(dashboard_id: int, limit: int = 100) -> List[Dict]:
    """Get all metrics for a dashboard"""
    ensure_storage()
    
    with open(METRICS_FILE, 'r') as f:
        data = json.load(f)
    
    metrics = [m for m in data['metrics'] if m['dashboard_id'] == dashboard_id]
    return metrics[-limit:] if limit else metrics

def get_latest_metrics(dashboard_id: int) -> List[Dict]:
    """Get latest metric values for each metric name"""
    ensure_storage()
    
    with open(METRICS_FILE, 'r') as f:
        data = json.load(f)
    
    metrics = [m for m in data['metrics'] if m['dashboard_id'] == dashboard_id]
    
    # Get latest value for each metric name
    latest = {}
    for m in sorted(metrics, key=lambda x: x['timestamp']):
        latest[m['name']] = m
    
    return list(latest.values())

def clear_old_metrics(days: int = 7):
    """Clear metrics older than specified days"""
    ensure_storage()
    
    from datetime import timedelta
    cutoff = datetime.now() - timedelta(days=days)
    
    with open(METRICS_FILE, 'r') as f:
        data = json.load(f)
    
    original_count = len(data['metrics'])
    data['metrics'] = [
        m for m in data['metrics'] 
        if datetime.fromisoformat(m['timestamp']) > cutoff
    ]
    
    with open(METRICS_FILE, 'w') as f:
        json.dump(data, f, indent=2)
    
    return original_count - len(data['metrics'])
