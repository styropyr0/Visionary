"""FastAPI routes for Visionary observability backend"""

from fastapi import APIRouter, Depends, HTTPException, Query
from typing import List, Optional, Dict
from pydantic import BaseModel
from core.registry import get_collector, list_collectors
from core.query_engine import QueryEngine
from core.models import Metric

router = APIRouter(prefix="/api/visionary", tags=["visionary"])


class MetricQuery(BaseModel):
    source: str
    filters: Optional[Dict[str, str]] = {}
    aggregation: Optional[str] = None  # sum, avg, min, max, p50, p95, p99
    limit: Optional[int] = None


class MetricData(BaseModel):
    name: str
    value: float
    labels: dict = {}
    timestamp: Optional[float] = None


class QueryResponse(BaseModel):
    source: str
    metrics: List[MetricData]
    count: int
    aggregate: Optional[Dict[str, float]] = None


@router.post("/query", response_model=QueryResponse)
async def query_metrics(query: MetricQuery):
    """
    Query metrics from a data source with advanced filtering and aggregation.

    Supports:
    - Filtering by metric name (exact or regex pattern)
    - Filtering by labels
    - Aggregation (sum, avg, min, max, stddev, median, percentiles)
    - Limiting results
    """
    try:
        collector_cls = get_collector(query.source)
        if not collector_cls:
            raise HTTPException(
                status_code=404, detail=f"Data source '{query.source}' not found"
            )

        # Collect metrics
        collector = collector_cls({})
        metrics = collector.collect()

        # Initialize query engine
        engine = QueryEngine()

        # Apply filtering
        filters = query.filters or {}
        filtered = engine.filter_metrics(
            metrics,
            name=filters.get('name'),
            name_pattern=filters.get('name_pattern'),
            labels=filters.get('labels'),
        )

        # Apply aggregation
        aggregate_result = None
        if query.aggregation:
            aggregate_result = {"value": engine.aggregate(filtered, func=query.aggregation)}

        # Apply sorting and limit
        if query.limit:
            filtered = engine.sort_by_value(filtered, ascending=False)[: query.limit]

        return QueryResponse(
            source=query.source,
            metrics=[
                MetricData(
                    name=m.name, value=m.value, labels=m.labels, timestamp=m.timestamp
                )
                for m in filtered
            ],
            count=len(filtered),
            aggregate=aggregate_result,
        )
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/sources")
async def list_sources():
    """List available data sources and their status"""
    sources = list_collectors()
    return {
        "sources": sources,
        "count": len(sources),
        "description": "List of all available data source connectors",
    }


@router.get("/sources/{source}/status")
async def source_status(source: str):
    """Check if a data source is available"""
    try:
        collector_cls = get_collector(source)
        if not collector_cls:
            return {"source": source, "available": False, "error": "Not found"}

        # Try to instantiate the collector
        collector = collector_cls({})
        metrics = collector.collect()
        return {
            "source": source,
            "available": True,
            "metrics_count": len(metrics),
        }
    except Exception as e:
        return {
            "source": source,
            "available": False,
            "error": str(e),
        }


@router.get("/health")
async def health_check():
    """Health check endpoint"""
    return {
        "status": "ok",
        "version": "0.1.0",
        "sources_available": len(list_collectors()),
    }

