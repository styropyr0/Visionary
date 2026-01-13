# Visionary Quick Start Guide

Get up and running with Visionary in 5 minutes.

## Prerequisites

- Python 3.8+
- pip

## Installation

### Option 1: Clone and Install

```bash
git clone https://github.com/yourusername/visionary.git
cd visionary
pip install -e .
```

### Option 2: Install from PyPI (Future)

```bash
pip install visionary
```

## Run the Demo

The simplest way to see Visionary in action:

```bash
python demo_server.py
```

This starts a server at **http://localhost:8000** with:
- Interactive metrics dashboard
- API documentation at `/api/visionary/docs`
- 15 available data source collectors

## First Query

1. Open http://localhost:8000 in your browser
2. In the **Data Sources** panel, click **system**
3. Click **Execute Query**
4. See your CPU, memory, and disk metrics displayed

Try these next:
- Enter `system.cpu*` in "Name Pattern" field to filter by regex
- Select **Average** in "Aggregation Function" to calculate average
- Set limit to **5** to see only top 5 metrics

## Query via API

Get metrics programmatically:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "system",
    "aggregate": "avg"
  }'
```

Response:
```json
{
  "source": "system",
  "count": 3,
  "aggregate": {"value": 26.77},
  "metrics": [
    {"name": "system.cpu_usage", "value": 0.0, "labels": {}},
    {"name": "system.memory_usage", "value": 72.6, "labels": {}},
    {"name": "system.disk_usage", "value": 7.7, "labels": {}}
  ]
}
```

## List Available Sources

```bash
curl http://localhost:8000/api/visionary/sources
```

Returns:
```json
{
  "sources": ["docker", "kubernetes", "prometheus", ...],
  "count": 15,
  "description": "List of all available data source connectors"
}
```

## Code Example

Use Visionary in Python:

```python
from core.registry import get_collector
from core.query_engine import QueryEngine

# Get system metrics
SystemCollector = get_collector('system')
collector = SystemCollector({})
metrics = collector.collect()

# Create query engine
engine = QueryEngine()

# Filter metrics
filtered = engine.filter_metrics(metrics, name_pattern='system.cpu*')

# Aggregate
avg = engine.aggregate(metrics, func='avg')
max_val = engine.aggregate(metrics, func='max')

# Group by label
by_device = engine.aggregate_by_groups(metrics, 'device', 'sum')

print(f"Average: {avg}")
print(f"Maximum: {max_val}")
print(f"By Device: {by_device}")
```

Output:
```
Average: 26.77
Maximum: 72.6
By Device: {'__unknown__': 80.3}
```

## Integrate with Django

```python
# settings.py
INSTALLED_APPS = [
    'visionary.django_app',
]

# urls.py
from django.urls import path, include
urlpatterns = [
    path('api/visionary/', include('visionary.django_app.urls')),
]
```

Then use the Django models and serializers:

```python
from django_app.models import Metric, Dashboard
from django_app.serializers import MetricSerializer

# Query all metrics
metrics = Metric.objects.all()
serializer = MetricSerializer(metrics, many=True)
print(serializer.data)
```

## Integrate with FastAPI

```python
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from fastapi_app.routes import router as visionary_router

app = FastAPI()

# Add CORS
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

# Add Visionary routes
app.include_router(visionary_router)

# Run: uvicorn main:app --reload
```

## Available Query Parameters

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "docker",                    # Required: data source name
    "name": "docker.container.running",    # Optional: exact metric name
    "name_pattern": "docker.container.*",  # Optional: regex pattern
    "labels": {"container": "api"},        # Optional: filter by labels
    "aggregate": "sum",                    # Optional: sum|avg|min|max|median|stddev|count|p50|p95|p99
    "group_by": "container",               # Optional: group by label key
    "limit": 10                            # Optional: max results
  }'
```

## Supported Data Sources

Without any configuration, try these:

| Source | Command | Notes |
|--------|---------|-------|
| **system** | Any | CPU, memory, disk metrics |
| **docker** | Requires Docker | Container and image metrics |
| **kubernetes** | Requires cluster access | Pod and node metrics |
| **prometheus** | Requires URL | Query Prometheus directly |
| **postgres** | Requires credentials | Database metrics |
| **mysql** | Requires credentials | Database metrics |
| **redis** | Requires URL | Cache metrics |
| **elasticsearch** | Requires URL | Search metrics |
| **influxdb** | Requires credentials | Time-series metrics |

For others (VMware, Hyper-V, KVM, OpenStack, Proxmox), provide configuration when initializing the collector.

## Next Steps

- **Read Full Docs**: See [Readme.md](Readme.md)
- **Explore API**: Visit http://localhost:8000/api/visionary/docs
- **Create Custom Collector**: See [Creating Custom Collectors](Readme.md#creating-custom-collectors) in README
- **Run Tests**: `python test_core.py`
- **Deploy**: See deployment guides in [docs/](docs/) folder

## Troubleshooting

### Server won't start
```bash
# Check if port 8000 is in use
lsof -i :8000  # macOS/Linux
netstat -ano | findstr :8000  # Windows

# Try different port
uvicorn demo_server:app --port 8001
```

### Import errors
```bash
# Reinstall with development dependencies
pip install -e ".[dev]"

# Run in debug mode
python -c "from core import QueryEngine; print(QueryEngine)"
```

### Collector returns no metrics
```bash
# Check collector availability
curl http://localhost:8000/api/visionary/sources/docker/status

# Verify configuration
# For example, Docker needs the socket at /var/run/docker.sock
```

### Web dashboard not loading
- Check browser console (F12) for errors
- Verify server is running on port 8000
- Try clearing browser cache (Ctrl+Shift+Delete)

## Getting Help

- **Issues**: Report bugs on GitHub Issues
- **Discussions**: Ask questions in GitHub Discussions
- **Docs**: Check [Readme.md](Readme.md) and [DEVELOPMENT_SUMMARY.md](DEVELOPMENT_SUMMARY.md)
- **Examples**: See code examples in [test_core.py](test_core.py)

---

**Happy querying! 🚀**
