# Visionary Django Integration

## Django URLs Configuration

### URL Structure

```
/api/visionary/
├── dashboards/
│   ├── POST   /api/visionary/dashboards/          → Create dashboard
│   ├── GET    /api/visionary/dashboards/<id>/     → Get dashboard details
│   ├── PUT    /api/visionary/dashboards/<id>/     → Update dashboard
│   └── DELETE /api/visionary/dashboards/<id>/     → Delete dashboard
```

### URL Definitions

**Root URLs** (`urls.py`):
```python
urlpatterns = [
    path('api/visionary/', include('django_app.urls')),
]
```

**App URLs** (`django_app/urls.py`):
```python
urlpatterns = [
    path('dashboards/', DashboardCreateView.as_view(), name='dashboard-create'),
    path('dashboards/<int:id>/', DashboardDetailView.as_view(), name='dashboard-detail'),
]
```

---

## Django API Endpoints

### 1. Create Dashboard
**Endpoint:** `POST /api/visionary/dashboards/`

**Request:**
```json
{
  "name": "Production Metrics",
  "description": "Monitor production system",
  "metrics": ["system.cpu_usage", "system.memory_usage"],
  "source": "system"
}
```

**Response:**
```json
{
  "code": 201,
  "message": "Dashboard created",
  "data": {
    "id": 1,
    "name": "Production Metrics",
    ...
  }
}
```

---

### 2. Get Dashboard
**Endpoint:** `GET /api/visionary/dashboards/<id>/`

**Response:**
```json
{
  "id": 1,
  "name": "Production Metrics",
  "description": "Monitor production system",
  "metrics": ["system.cpu_usage", "system.memory_usage"],
  "source": "system",
  "created_at": "2026-01-13T10:30:00Z"
}
```

---

### 3. Update Dashboard
**Endpoint:** `PUT /api/visionary/dashboards/<id>/`

**Request:**
```json
{
  "name": "Updated Dashboard Name",
  "metrics": ["system.cpu_usage", "system.memory_usage", "system.disk_usage"]
}
```

**Response:**
```json
{
  "code": 200,
  "message": "Dashboard updated"
}
```

---

### 4. Delete Dashboard
**Endpoint:** `DELETE /api/visionary/dashboards/<id>/`

**Response:**
```json
{
  "code": 204,
  "message": "Dashboard deleted"
}
```

---

## Django Models

### Dashboard Model
```python
class Dashboard(models.Model):
    name = models.CharField(max_length=255)
    description = models.TextField(blank=True)
    metrics = models.JSONField()  # List of metric names
    source = models.CharField(max_length=100)  # system, postgres, mysql, etc.
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    
    def __str__(self):
        return self.name
```

---

## Using Visionary with Django

### In Views
```python
from django_app.models import Dashboard
from core.registry import get_collector
from core.query_engine import QueryEngine

def dashboard_metrics_view(request, dashboard_id):
    # Get dashboard config
    dashboard = Dashboard.objects.get(id=dashboard_id)
    
    # Collect metrics
    collector = get_collector(dashboard.source)({})
    metrics = collector.collect()
    
    # Filter to dashboard metrics
    engine = QueryEngine()
    filtered = engine.filter_metrics(metrics, name_list=dashboard.metrics)
    
    return JsonResponse({
        'dashboard': dashboard.name,
        'metrics': [m.to_dict() for m in filtered]
    })
```

### In Management Commands
```bash
# List all data sources
python manage.py collect_metrics --list

# Collect CPU metrics
python manage.py collect_metrics --source system --pattern "system.cpu.*"

# Get average of memory metrics
python manage.py collect_metrics --source system --pattern "system.memory.*" --aggregation avg

# Show top 10 metrics
python manage.py collect_metrics --source system --limit 10
```

### In Celery Tasks
```python
from celery import shared_task
from core.registry import get_collector
from django_app.models import Dashboard

@shared_task
def collect_dashboard_metrics(dashboard_id):
    dashboard = Dashboard.objects.get(id=dashboard_id)
    collector = get_collector(dashboard.source)({})
    metrics = collector.collect()
    
    # Store in cache or database
    from django.core.cache import cache
    cache.set(f'dashboard_{dashboard_id}', metrics, 300)
    
    return f"Collected {len(metrics)} metrics for dashboard {dashboard.name}"
```

### In Django Admin
```python
from django.contrib import admin
from .models import Dashboard

@admin.register(Dashboard)
class DashboardAdmin(admin.ModelAdmin):
    list_display = ('name', 'source', 'created_at')
    list_filter = ('source', 'created_at')
    search_fields = ('name', 'description')
    readonly_fields = ('created_at', 'updated_at')
```

---

## Django vs FastAPI

### FastAPI (Current Demo)
- **Type:** REST API
- **URLs:** `/api/visionary/query`, `/api/visionary/sources`
- **Data Storage:** None (in-memory)
- **Use Case:** Standalone metrics API

### Django Integration
- **Type:** Web Framework
- **URLs:** `/api/visionary/dashboards/*`
- **Data Storage:** Database (models, migrations)
- **Use Case:** Save metric configurations, user dashboards

---

## Running Django

### Setup
```bash
# Create migrations
python manage.py makemigrations

# Apply migrations
python manage.py migrate

# Create superuser
python manage.py createsuperuser
```

### Run Development Server
```bash
python manage.py runserver
```

### Access
- Admin: http://localhost:8000/admin
- API: http://localhost:8000/api/visionary/dashboards/
- Docs: http://localhost:8000/api/visionary/docs (if using DRF)

---

## Full Request/Response Examples

### Create Dashboard (cURL)
```bash
curl -X POST http://localhost:8000/api/visionary/dashboards/ \
  -H "Content-Type: application/json" \
  -d '{
    "name": "System Monitor",
    "description": "Monitor system health",
    "metrics": ["system.cpu_usage", "system.memory_usage"],
    "source": "system"
  }'
```

### Get Dashboard (cURL)
```bash
curl http://localhost:8000/api/visionary/dashboards/1/
```

### Update Dashboard (cURL)
```bash
curl -X PUT http://localhost:8000/api/visionary/dashboards/1/ \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Updated Monitor"
  }'
```

### Delete Dashboard (cURL)
```bash
curl -X DELETE http://localhost:8000/api/visionary/dashboards/1/
```

---

## Django Architecture

```
Visionary Django Integration
├── django_app/
│   ├── models.py          → Dashboard model definition
│   ├── views.py           → DashboardCreateView, DashboardDetailView
│   ├── serializers.py     → DashboardSerializer
│   ├── urls.py            → URL routing (NEW)
│   ├── admin.py           → Django admin config
│   ├── apps.py            → App config
│   ├── migrations/        → Database migrations
│   └── management/
│       └── commands/
│           └── collect_metrics.py  → Management command (NEW)
├── urls.py                → Root URL config (NEW)
└── settings_example.py    → Django settings template (NEW)
```

---

## Integration with FastAPI + Django

You can run **both** simultaneously:

```bash
# Terminal 1: FastAPI (interactive dashboard)
python demo_server.py

# Terminal 2: Django (persistent dashboards)
python manage.py runserver 8001
```

Then:
- **FastAPI at** http://localhost:8000 — Ad-hoc queries
- **Django at** http://localhost:8001 — Saved dashboard configurations
