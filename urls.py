"""
Root Django URL Configuration
Include all app URLs here
"""

from django.contrib import admin
from django.urls import path, include

urlpatterns = [
    # Django admin
    path('admin/', admin.site.urls),
    
    # Visionary API endpoints
    path('api/visionary/', include('django_app.urls')),
]
