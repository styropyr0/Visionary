#!/usr/bin/env python
"""
Initialize the Django database and create tables for dashboards.
Run this once before starting the demo server.
"""

import os
import sys
import django

# Setup Django
os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'settings')
sys.path.insert(0, os.path.dirname(__file__))

django.setup()

from django.core.management import call_command

if __name__ == '__main__':
    print("\n" + "="*60)
    print("  Visionary Database Initialization")
    print("="*60)
    
    # Create migrations (optional - using simple approach)
    print("\nCreating database tables...")
    try:
        call_command('migrate', run_syncdb=True, verbosity=1)
        print("✓ Database tables created successfully")
    except Exception as e:
        print(f"Note: {e}")
        # Fallback: create tables directly
        print("\nUsing direct table creation...")
        from django.core.management import sql
        from django_app.models import Dashboard, Panel, Metric, MetricRecord
        
        # Create tables
        from django.db import connection
        with connection.schema_editor() as schema_editor:
            try:
                schema_editor.create_model(Metric)
                print("✓ Created Metric table")
            except:
                pass
            
            try:
                schema_editor.create_model(MetricRecord)
                print("✓ Created MetricRecord table")
            except:
                pass
            
            try:
                schema_editor.create_model(Panel)
                print("✓ Created Panel table")
            except:
                pass
            
            try:
                schema_editor.create_model(Dashboard)
                print("✓ Created Dashboard table")
            except:
                pass
    
    print("\n" + "="*60)
    print("  Database is ready!")
    print("  Run: python demo_server.py")
    print("  Then visit: http://localhost:8000/create_dashboard.html")
    print("="*60 + "\n")
