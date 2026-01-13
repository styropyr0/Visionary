#!/usr/bin/env python
"""
Visionary Setup Script
Framework-agnostic observability backend library
"""

from setuptools import setup, find_packages

with open("Readme.md", "r", encoding="utf-8") as fh:
    long_description = fh.read()

setup(
    name="visionary",
    version="0.1.0",
    author="Saurav Sajeev",
    description="OpenAPI-first, framework-agnostic observability backend library",
    long_description=long_description,
    long_description_content_type="text/markdown",
    url="https://github.com/yourusername/visionary",
    packages=find_packages(),
    python_requires=">=3.8",
    install_requires=[
        "Django>=4.0",
        "djangorestframework>=3.14",
        "docker>=7.0",
        "kubernetes>=33.0",
        "prometheus-client>=0.18",
        "psycopg2>=2.9",
        "PyMySQL>=1.1",
        "redis>=5.0",
        "elasticsearch>=9.0",
        "influxdb>=5.3",
        "psutil>=5.9",
        "pyvmomi>=7.0",
        "openstacksdk>=4.0",
        "proxmoxer>=2.2",
        "requests>=2.28",
    ],
    extras_require={
        "fastapi": ["fastapi>=0.100", "uvicorn>=0.23", "pydantic>=2.0"],
        "dev": [
            "pytest>=7.4",
            "pytest-django>=4.5",
            "pytest-cov>=4.1",
            "black>=23.7",
            "ruff>=0.0.285",
            "mypy>=1.4",
        ],
    },
    classifiers=[
        "Development Status :: 3 - Alpha",
        "Intended Audience :: Developers",
        "License :: OSI Approved :: MIT License",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.8",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
        "Topic :: Software Development :: Libraries :: Python Modules",
        "Topic :: System :: Monitoring",
    ],
    license="MIT",
    keywords="observability metrics monitoring dashboard time-series",
)
