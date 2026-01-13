"""
Django URL Configuration for Visionary
Maps API endpoints to view handlers
"""

from django.urls import path
from .views import DashboardCreateView, DashboardDetailView

app_name = 'visionary'

urlpatterns = [
    # Dashboard management endpoints
    path('dashboards/', DashboardCreateView.as_view(), name='dashboard-create'),
    path('dashboards/<int:id>/', DashboardDetailView.as_view(), name='dashboard-detail'),
    
    # Future endpoints can be added here
    # path('metrics/', MetricsListView.as_view(), name='metrics-list'),
    # path('metrics/<int:id>/', MetricsDetailView.as_view(), name='metrics-detail'),
]
