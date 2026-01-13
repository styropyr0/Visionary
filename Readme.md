# Visionary

Visionary is an **OpenAPI-first, framework-agnostic observability backend library** that enables developers to build custom dashboards, panels, and metrics integrations with Django or FastAPI (and other frameworks) using a shared core logic.

**Status:** v0.1.0 (Alpha) - Core functionality complete, production-ready for custom integrations

---

## What is Visionary?

Unlike traditional monitoring tools like Grafana, Visionary is a **metrics SDK** that you integrate into your Python application. It provides:

- **Pluggable data source collectors** — pull metrics from 15+ platforms (Docker, Kubernetes, Prometheus, databases, cloud platforms, etc.)
- **Advanced query engine** — filter, group, aggregate, and analyze metrics in-memory
- **Framework integration** — Django REST API or FastAPI routers out of the box
- **OpenAPI documentation** — auto-generated API schema for all endpoints

Perfect for:
- Internal dashboards in SaaS applications
- Embedded observability in monitoring tools
- Cost-conscious infrastructure monitoring
- CI/CD pipeline metrics and automation
- Building custom analytics over infrastructure data

---

## Core Features

- **Framework-agnostic core** — Shared data models, collectors, and query engine
- **Multi-source collectors** — 15 pre-built collectors for:
  - **Databases**: PostgreSQL, MySQL, Redis, Elasticsearch, InfluxDB
  - **Time-series**: Prometheus
  - **System**: CPU, memory, disk (via psutil)
  - **Container & virtualization**: Docker, Kubernetes, VMware, Hyper-V, KVM, Xen, OpenStack, Proxmox
- **Advanced Query Engine**:
  - Filtering by metric name, regex patterns, labels, time ranges
  - Aggregations: sum, avg, min, max, median, stddev, percentiles (p50, p95, p99)
  - Grouping and multi-level aggregations
  - Threshold checking and rate-of-change calculations
  - Sorting, limiting, and top-N queries
- **Framework integrations**:
  - **Django**: REST API views, models, serializers
  - **FastAPI**: OpenAPI-compatible routers, Pydantic schemas
- **Plug-in registry** — Dynamically register custom collectors
- **Response helpers** — Standardized success/error responses
- **Web UI** — Minimal web dashboard for ad-hoc queries (included)

---

## Quick Start

### Installation

```bash
pip install visionary
```

Or from source:

```bash
git clone https://github.com/yourusername/visionary.git
cd visionary
pip install -e .
```

### Run the Demo Server

```bash
python demo_server.py
```

Then open **http://localhost:8000** in your browser to access the dashboard.

Try querying system metrics:
1. Select **system** from the Data Sources list
2. Leave filters empty and click **Execute Query**
3. See CPU, memory, and disk metrics displayed

### Query Metrics in Python

```python
from core.registry import get_collector
from core.query_engine import QueryEngine

# Load a collector
SystemCollector = get_collector('system')
collector = SystemCollector({})
metrics = collector.collect()

# Use query engine for advanced operations
engine = QueryEngine()

# Filter by name
cpu_metrics = engine.filter_metrics(metrics, name='system.cpu_usage')

# Aggregate
avg_cpu = engine.aggregate(metrics, func='avg')
max_cpu = engine.aggregate(metrics, func='max')

# Group and aggregate
by_device = engine.aggregate_by_groups(metrics, group_by='device', func='sum')
```

### Django Integration

```python
# settings.py
INSTALLED_APPS = [
    'django.contrib.contenttypes',
    'django.contrib.auth',
    'rest_framework',
    'visionary.django_app',
]

# urls.py
from django.urls import include, path

urlpatterns = [
    path('api/visionary/', include('visionary.django_app.urls')),
]
```

### FastAPI Integration

```python
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from fastapi_app.routes import router as visionary_router

app = FastAPI()

# Enable CORS for cross-origin requests
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

# Include Visionary API
app.include_router(visionary_router)

# Run: uvicorn main:app --reload
```

---

## API Reference

### Query Endpoint (POST /api/visionary/query)

Execute advanced queries on metric data:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "system",
    "name": "system.cpu_usage",
    "aggregate": "avg",
    "limit": 10
  }'
```

**Query Parameters:**
- `source` (required): Data source name (e.g., "system", "docker", "kubernetes")
- `name` (optional): Exact metric name match
- `name_pattern` (optional): Regex pattern for metric name
- `labels` (optional): Object of label key-value pairs to filter by
- `aggregate` (optional): Aggregation function (sum, avg, min, max, median, stddev, count, p50, p95, p99)
- `group_by` (optional): Label key to group results by
- `limit` (optional): Maximum number of metrics to return

**Response:**
```json
{
  "source": "system",
  "count": 3,
  "metrics": [
    {
      "name": "system.cpu_usage",
      "value": 0.0,
      "labels": {},
      "timestamp": 1234567890
    }
  ],
  "aggregate": {
    "value": 26.77
  }
}
```

### List Sources Endpoint (GET /api/visionary/sources)

```bash
curl http://localhost:8000/api/visionary/sources
```

Returns:
```json
{
  "sources": ["docker", "elasticsearch", "hyperv", "influxdb", "kubernetes", ...],
  "count": 15,
  "description": "List of all available data source connectors"
}
```

### Health Check (GET /api/visionary/health)

```bash
curl http://localhost:8000/api/visionary/health
```

---

## Project Structure

```
visionary/
├── core/                      # Core library
│   ├── __init__.py           # Package exports
│   ├── base.py               # BaseCollector abstract class
│   ├── models.py             # Metric, Panel, Dashboard models
│   ├── query_engine.py       # Advanced query engine
│   ├── registry.py           # Dynamic collector registry (lazy loading)
│   ├── settings.py           # Configuration
│   ├── data_sources/         # 15 pre-built collectors
│   ├── exceptions/           # Custom exceptions
│   └── response/             # Response helpers
├── django_app/               # Django integration
│   ├── __init__.py
│   ├── models.py
│   ├── views.py
│   ├── serializers.py
│   └── migrations/
├── fastapi_app/              # FastAPI integration
│   ├── __init__.py
│   └── routes.py
├── ui/                       # Web UI (static HTML + JS)
│   └── index.html           # Interactive dashboard
├── setup.py                  # Package configuration
├── pyproject.toml            # Modern Python packaging
├── requirements.txt          # Legacy dependencies file
├── demo_server.py            # Run example: python demo_server.py
├── test_core.py              # Test script
└── README.md                 # This file
```

---

## Query Engine Capabilities

The `QueryEngine` supports advanced metric operations:

### Filtering
```python
engine = QueryEngine()

# Exact name match
engine.filter_metrics(metrics, name='system.cpu_usage')

# Regex pattern
engine.filter_metrics(metrics, name_pattern='system\.*')

# By labels
engine.filter_metrics(metrics, labels={'device': 'eth0'})

# Time range
engine.filter_metrics(
    metrics,
    timestamp_from=1609459200,  # Jan 1, 2021
    timestamp_to=1640995200      # Jan 1, 2022
)
```

### Aggregation
```python
# Single aggregation
engine.aggregate(metrics, func='avg')          # Average
engine.aggregate(metrics, func='sum')          # Sum
engine.aggregate(metrics, func='max')          # Maximum
engine.aggregate(metrics, func='median')       # Median
engine.aggregate(metrics, func='stddev')       # Std Deviation
engine.aggregate(metrics, func='p95')          # 95th percentile

# Group and aggregate
engine.aggregate_by_groups(metrics, group_by='device', func='avg')
```

### Sorting & Selection
```python
engine.sort_by_value(metrics, ascending=False)
engine.top_n(metrics, n=10)
engine.bottom_n(metrics, n=5)
engine.limit(metrics, limit=100)
```

### Threshold & Analysis
```python
engine.apply_threshold(metrics, lower=50, upper=80)  # Categorize by thresholds
engine.rate_of_change(current_metrics, previous_metrics)  # Calculate deltas
```

---

## Available Data Sources

| Source | Module | Status | Config |
|--------|--------|--------|--------|
| System | `system.py` | ✅ Stable | None (uses psutil) |
| Docker | `docker.py` | ✅ Stable | Docker socket path |
| Kubernetes | `kubernetes.py` | ✅ Stable | Kubeconfig path |
| Prometheus | `prometheus.py` | ✅ Stable | URL, username, password |
| PostgreSQL | `postgres.py` | ✅ Stable | Host, port, user, password, database |
| MySQL | `mysql.py` | ✅ Stable | Host, port, user, password, database |
| Redis | `redis.py` | ✅ Stable | Host, port |
| Elasticsearch | `elastic_search.py` | ✅ Stable | Host, port, username, password |
| InfluxDB | `influxdb.py` | ✅ Stable | Host, port, token, org, bucket |
| VMware | `vmware.py` | ✅ Stable | Host, user, password |
| Hyper-V | `hyperv.py` | ✅ Windows | Local (WMI) |
| KVM | `kvm.py` | ✅ Stable | Libvirt URI |
| Xen | `xen.py` | ✅ Stable | Xend host, port |
| OpenStack | `openstack.py` | ✅ Stable | Auth URL, project, user, password |
| Proxmox | `proxmox.py` | ✅ Stable | Host, user, password |

---

## Creating Custom Collectors

Extend Visionary with custom data sources:

```python
from core.base import BaseCollector
from core.models import Metric

class MyCollector(BaseCollector):
    def collect(self):
        # Your logic to fetch metrics
        return [
            Metric(
                name='my_metric.counter',
                value=42,
                labels={'service': 'api'},
            )
        ]

# Register it
from core.registry import register_collector
register_collector('my_source', MyCollector)

# Use it
from core.registry import get_collector
MyCollector = get_collector('my_source')
metrics = MyCollector({}).collect()
```

---

## Roadmap

- [x] Core library with 15 collectors
- [x] Advanced query engine with aggregations
- [x] Framework integrations (Django, FastAPI)
- [x] Web UI (minimal dashboard)
- [ ] Authentication & role-based access control
- [ ] Time-series storage (optional InfluxDB backend)
- [ ] Alert rules and notifications
- [ ] Prometheus metrics exporter format
- [ ] Grafana datasource plugin
- [ ] Kubernetes operator & Helm chart
- [ ] Performance benchmarks and optimization

---

## Contributing

Contributions are welcome! Areas for help:

- Adding new data source collectors
- Improving performance and caching
- Building more advanced UI features
- Documentation and examples
- Integration with other frameworks (Starlette, Quart, etc.)

To contribute:
1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Commit changes (`git commit -am 'Add my feature'`)
4. Push to branch (`git push origin feature/my-feature`)
5. Open a Pull Request

---

## License

Licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

---

## Support

- **Issues**: Open an issue on GitHub
- **Discussions**: Use GitHub Discussions for questions
- **Documentation**: Check [docs/](docs/) folder for detailed guides

---

## Acknowledgments

Built with inspiration from Prometheus, Grafana, and the broader observability community.

