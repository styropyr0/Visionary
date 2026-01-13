"""FastAPI integration for Visionary observability backend"""

from fastapi import APIRouter
from typing import List
from pydantic import BaseModel

router = APIRouter(prefix="/api/visionary", tags=["visionary"])


class MetricResponse(BaseModel):
    name: str
    value: float
    labels: dict = {}
    timestamp: float = None

    class Config:
        from_attributes = True


class PanelResponse(BaseModel):
    id: int
    title: str
    panel_type: str
    metrics: List[MetricResponse] = []

    class Config:
        from_attributes = True


class DashboardResponse(BaseModel):
    id: int
    name: str
    panels: List[PanelResponse] = []

    class Config:
        from_attributes = True


@router.get("/metrics")
async def list_metrics() -> List[MetricResponse]:
    """List all available metrics"""
    return []


@router.get("/panels")
async def list_panels() -> List[PanelResponse]:
    """List all available panels"""
    return []


@router.get("/dashboards")
async def list_dashboards() -> List[DashboardResponse]:
    """List all available dashboards"""
    return []


@router.get("/panels/{panel_id}")
async def get_panel(panel_id: int) -> PanelResponse:
    """Get a specific panel"""
    return {}


@router.get("/dashboards/{dashboard_id}")
async def get_dashboard(dashboard_id: int) -> DashboardResponse:
    """Get a specific dashboard"""
    return {}
