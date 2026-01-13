#!/usr/bin/env python
"""
Visionary Demo Server
Runs FastAPI backend with CORS enabled for the UI
"""

import uvicorn
from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware
import os
import sys

# Add project root to path
sys.path.insert(0, os.path.dirname(__file__))

from fastapi_app.routes import router as visionary_router

# Create FastAPI app
app = FastAPI(
    title="Visionary Metrics API",
    description="OpenAPI-first observability backend library",
    version="0.1.0",
    openapi_url="/api/visionary/openapi.json",
    docs_url="/api/visionary/docs",
    redoc_url="/api/visionary/redoc",
)

# Add CORS middleware to allow requests from the UI
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # Allow all origins for development
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Include Visionary router FIRST (before static files)
app.include_router(visionary_router)

# Mount static files (UI) LAST so API routes take priority
ui_path = os.path.join(os.path.dirname(__file__), "ui")
if os.path.exists(ui_path):
    app.mount("/", StaticFiles(directory=ui_path, html=True), name="static")


if __name__ == "__main__":
    print("\n" + "="*60)
    print("  Visionary Metrics Backend - Demo Server")
    print("="*60)
    print("\n  API Endpoints:")
    print("    - http://localhost:8000/api/visionary/docs (Swagger UI)")
    print("    - http://localhost:8000/api/visionary/redoc (ReDoc)")
    print("    - http://localhost:8000 (Web Dashboard)")
    print("\n  Starting server...\n")

    uvicorn.run(
        app,
        host="0.0.0.0",
        port=8000,
        log_level="info",
    )
