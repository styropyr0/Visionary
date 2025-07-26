# Visionary

Visionary is an OpenAPI-first, framework-agnostic observability backend library that enables developers to build custom dashboards, panels, and metrics integrations with Django or FastAPI (and other frameworks) using a shared core logic.

Status: Under Development

---

## Core Features

* Framework-agnostic core: Shared data models, collectors, and query engine in `visionary/core/`
* Django integration: REST API views, serializers, and models in `visionary/django_app/`
* FastAPI integration: Routers and Pydantic schemas in `visionary/fastapi_app/`
* Metric collectors for:

  * Databases: PostgreSQL, MySQL, Redis, Elasticsearch, InfluxDB
  * Time-series: Prometheus
  * System: CPU, memory, disk (via psutil)
  * Virtualization and cloud platforms: Kubernetes, Docker, VMware, Hyper-V, KVM, Xen, OpenStack, Proxmox, Railway
* Plug-in registry: Dynamically register custom collectors via `registry.register_collector()`
* Query engine: In-memory filtering and grouping of metrics
* OpenAPI documentation: Auto-generated schema for all endpoints (Swagger and Redoc)

---

## Installation

```bash
pip install visionary
```

In a Django project:

```python
# settings.py
INSTALLED_APPS += [
  'visionary.django_app',
]

# urls.py
from django.urls import include, path

urlpatterns = [
    path('api/visionary/', include('visionary.django_app.urls')),
]
```

In a FastAPI project:

```python
from fastapi import FastAPI
from visionary.fastapi_app.routes import router as visionary_router

app = FastAPI(
    title="Visionary API",
    openapi_url="/api/visionary/openapi.json"
)
app.include_router(visionary_router, prefix="/api/visionary")
```

---

## Project Structure

```
visionary/
├── core/             # Shared models, collectors, registry, settings
├── django_app/       # Django integration (models, serializers, views)
├── fastapi_app/      # FastAPI integration (routers, schemas)
├── tests/            # Unit and integration tests
├── setup.py          # PyPI packaging configuration
└── README.md         # This file
```

---

## Usage Examples

### Collecting Metrics in Python

```python
from visionary.core.registry import get_collector

config = {
    'host': 'db.local',
    'port': 5432,
    'user': 'monitor',
    'password': 'secret',
    'database': 'app_db'
}
collector = get_collector('postgres')(config)
metrics = collector.collect()
```

### Django API Endpoints

| Endpoint                      | Method | Description                         |
| ----------------------------- | ------ | ----------------------------------- |
| `/api/visionary/panels/`      | GET    | List or create panels               |
| `/api/visionary/panels/{id}/` | GET    | Retrieve, update, or delete a panel |
| `/api/visionary/metrics/`     | GET    | List collected metric records       |

---

## Roadmap

* Complete core collectors with additional metrics and pagination
* Implement FastAPI schemas and routers
* Add authentication and role-based access control
* Auto-discovery of data sources and default dashboards
* Plugin development SDK and examples
* Performance optimization with native extensions
* Publish to PyPI

---

## Contributing

Contributions are welcome. Please open issues or submit pull requests against this repository.

---

## License

This project is licensed under the MIT License. See the LICENSE file for details.
