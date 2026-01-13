# Visionary API Reference

## Base URL
```
http://localhost:8000/api/visionary
```

## Endpoints

### 1. Query Metrics (Primary Endpoint)

**Endpoint:** `POST /api/visionary/query`

Execute advanced queries on metrics with filtering, aggregation, and grouping.

#### Request Example

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

#### Request Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `source` | string | Yes | Data source name (docker, kubernetes, prometheus, system, etc.) |
| `name` | string | No | Exact metric name match |
| `name_pattern` | string | No | Regex pattern for metric name (e.g., "system.*") |
| `labels` | object | No | Filter by labels: `{"key": "value"}` |
| `aggregate` | string | No | Aggregation function: sum, avg, min, max, median, stddev, count, p50, p95, p99 |
| `group_by` | string | No | Group results by label key |
| `limit` | integer | No | Maximum metrics to return |

#### Response Example

```json
{
  "source": "system",
  "count": 3,
  "metrics": [
    {
      "name": "system.cpu_usage",
      "value": 0.0,
      "labels": {},
      "timestamp": 1673539200.5
    },
    {
      "name": "system.memory_usage",
      "value": 72.6,
      "labels": {},
      "timestamp": 1673539200.5
    },
    {
      "name": "system.disk_usage",
      "value": 7.7,
      "labels": {},
      "timestamp": 1673539200.5
    }
  ],
  "aggregate": {
    "value": 26.77
  }
}
```

---

### 2. List Data Sources

**Endpoint:** `GET /api/visionary/sources`

Get list of all available data source collectors.

#### Request Example

```bash
curl http://localhost:8000/api/visionary/sources
```

#### Response Example

```json
{
  "sources": [
    "docker",
    "elasticsearch",
    "hyperv",
    "influxdb",
    "kubernetes",
    "kvm",
    "mysql",
    "openstack",
    "postgres",
    "prometheus",
    "proxmox",
    "redis",
    "system",
    "vmware",
    "xen"
  ],
  "count": 15,
  "description": "List of all available data source connectors"
}
```

---

### 3. Check Source Status

**Endpoint:** `GET /api/visionary/sources/{source}/status`

Check if a data source is available and can connect.

#### Request Example

```bash
curl http://localhost:8000/api/visionary/sources/docker/status
```

#### Response Examples

**Success (200):**
```json
{
  "source": "docker",
  "available": true,
  "metrics_count": 42
}
```

**Failure:**
```json
{
  "source": "docker",
  "available": false,
  "error": "Cannot connect to Docker daemon"
}
```

---

### 4. Health Check

**Endpoint:** `GET /api/visionary/health`

Check server health and availability.

#### Request Example

```bash
curl http://localhost:8000/api/visionary/health
```

#### Response Example

```json
{
  "status": "ok",
  "version": "0.1.0",
  "sources_available": 15
}
```

---

## Query Examples

### Example 1: Simple Metric Query

Get all system metrics:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{"source": "system"}'
```

### Example 2: Filter by Name Pattern

Get all Docker container metrics:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "docker",
    "name_pattern": "docker.container.*"
  }'
```

### Example 3: Aggregation

Get average of all system metrics:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "system",
    "aggregate": "avg"
  }'
```

### Example 4: Multiple Percentiles

Get 95th percentile for memory metrics:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "prometheus",
    "name_pattern": ".*memory.*",
    "aggregate": "p95",
    "limit": 20
  }'
```

### Example 5: Group and Aggregate

Get total (sum) metrics grouped by container:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "docker",
    "group_by": "container",
    "aggregate": "sum"
  }'
```

### Example 6: Label Filtering

Get metrics for specific network interface:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "system",
    "name_pattern": "system.network.*",
    "labels": {"interface": "eth0"}
  }'
```

### Example 7: Limit Results

Get top 10 highest disk usage metrics:

```bash
curl -X POST http://localhost:8000/api/visionary/query \
  -H "Content-Type: application/json" \
  -d '{
    "source": "system",
    "name": "system.disk_usage",
    "limit": 10
  }'
```

---

## Response Status Codes

| Code | Meaning |
|------|---------|
| 200 | Success — Query executed successfully |
| 400 | Bad Request — Invalid parameters |
| 404 | Not Found — Data source doesn't exist |
| 500 | Server Error — Internal error occurred |

## Error Responses

```json
{
  "detail": "Data source 'invalid_source' not found"
}
```

---

## Aggregation Functions

| Function | Description | Example |
|----------|-------------|---------|
| `sum` | Sum of all values | Total bytes transferred |
| `avg` | Average of all values | Average CPU usage |
| `min` | Minimum value | Lowest memory usage |
| `max` | Maximum value | Peak memory usage |
| `median` | Middle value | Median response time |
| `stddev` | Standard deviation | Variance in metrics |
| `count` | Number of metrics | Total metrics collected |
| `p50` | 50th percentile | Median |
| `p95` | 95th percentile | 95% of values are below |
| `p99` | 99th percentile | 99% of values are below |

---

## Data Source Configuration

Most data sources work without configuration. Some require credentials:

### PostgreSQL
```python
config = {
    'host': 'localhost',
    'port': 5432,
    'user': 'postgres',
    'password': 'secret',
    'database': 'mydb'
}
```

### MySQL
```python
config = {
    'host': 'localhost',
    'port': 3306,
    'user': 'root',
    'password': 'secret',
    'database': 'mydb'
}
```

### Prometheus
```python
config = {
    'host': 'localhost',
    'port': 9090,
    'url': 'http://localhost:9090'
}
```

### Docker
```python
config = {
    'socket_url': 'unix:///var/run/docker.sock'
}
```

### Kubernetes
```python
config = {
    'kubeconfig': '/path/to/kubeconfig'
}
```

---

## Response Schema

All metric responses follow this schema:

```json
{
  "source": "string",           // Data source name
  "count": "integer",           // Number of metrics returned
  "metrics": [
    {
      "name": "string",         // Metric name
      "value": "number",        // Numeric value
      "labels": "object",       // Key-value labels
      "timestamp": "float"      // Unix timestamp (seconds)
    }
  ],
  "aggregate": "object|null"    // Aggregation results (if requested)
}
```

---

## Rate Limiting

Currently no rate limiting is implemented. For production deployment, consider:
- Adding rate limiting middleware
- Implementing caching for frequently requested metrics
- Setting query timeouts

---

## Authentication

Currently no authentication is required. For production:
1. Add API key validation
2. Implement JWT tokens
3. Add role-based access control (RBAC)
4. Use OAuth 2.0 for third-party access

---

## WebSocket Support (Planned)

Future versions may support WebSocket streaming for real-time metrics:

```bash
ws://localhost:8000/api/visionary/stream/system
```

---

## Documentation

- **Interactive API Docs**: http://localhost:8000/api/visionary/docs
- **Alternative Format**: http://localhost:8000/api/visionary/redoc
- **GitHub**: https://github.com/yourusername/visionary

---

## SDK Usage

Instead of curl, use the Python SDK:

```python
from core.registry import get_collector
from core.query_engine import QueryEngine

# Get metrics
collector = get_collector('docker')({})
metrics = collector.collect()

# Query
engine = QueryEngine()
filtered = engine.filter_metrics(metrics, name_pattern='docker.container.*')
avg = engine.aggregate(filtered, 'avg')
```

---

**API Version:** 0.1.0  
**Last Updated:** January 2026
