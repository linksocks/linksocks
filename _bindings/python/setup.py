#!/usr/bin/env python3
"""
Setup script for wssocks Python bindings.

WSSocks is a SOCKS proxy implementation over WebSocket protocol.
This package provides Python bindings for the Go implementation.
"""

from pathlib import Path
from setuptools import setup, find_packages
import setuptools

# Get the current directory
here = Path(__file__).parent.absolute()
# Get project root directory (two levels up from bindings/python/)
project_root = here.parent.parent

def read_file(filename):
    """Read content from file."""
    file_path = project_root / filename
    if not file_path.exists():
        raise FileNotFoundError(f"Required file not found: {file_path}")
    with open(file_path, "r", encoding="utf-8") as f:
        return f.read()

def read_requirements(filename="requirements.txt"):
    """Read requirements from file."""
    req_file = here / filename
    if req_file.exists():
        with open(req_file, "r", encoding="utf-8") as f:
            return [line.strip() for line in f if line.strip() and not line.startswith("#")]
    return []

# Read description from README
def get_long_description():
    """Get long description from README file."""
    return read_file("README.md")

# Platform-specific configurations
install_requires = [
    "setuptools>=40.0",
    "click>=8.0",
    "loguru",
    "rich",
]

# Development dependencies
extras_require = {
    "dev": [
        "pytest>=6.0",
        "pytest-cov>=2.10",
        "pytest-mock>=3.0",
        "black>=21.0",
        "flake8>=3.8",
        "mypy>=0.800",
    ],
    "test": [
        "pytest>=6.0",
        "pytest-cov>=2.10",
        "pytest-mock>=3.0",
        "pytest-xdist",
    ],
}

# Ensure the wssocks package directory exists
wssocks_bindings_dir = here / "wssockslib"
if not wssocks_bindings_dir.exists():
    raise RuntimeError(
        f"wssocks bindings package directory not found at {wssocks_bindings_dir}. "
        "Please run 'make python-bindings' first to generate the Python bindings."
    )

# Check for required files
required_files = [
    wssocks_bindings_dir / "__init__.py",
    wssocks_bindings_dir / "wssocks.py",
]

missing_files = [f for f in required_files if not f.exists()]
if missing_files:
    raise RuntimeError(
        f"Required files missing: {missing_files}. "
        "Please run 'make python-bindings' first to generate the Python bindings."
    )

# Find all Python files in the wssocks package
package_data = {}
wssocks_files = []

# Include all necessary files from the wssocks directory
for ext in ["*.py", "*.so", "*.h", "*.c", "*.go"]:
    wssocks_files.extend([
        str(p.relative_to(wssocks_bindings_dir))
        for p in wssocks_bindings_dir.glob(ext)
        if p.is_file()
    ])

if wssocks_files:
    package_data["wssocks"] = wssocks_files

class BinaryDistribution(setuptools.Distribution):
    def has_ext_modules(_):
        return True

setup(
    name="wssocks",
    version="1.6.5",
    description="Python bindings for WSSocks - SOCKS proxy over WebSocket",
    long_description=get_long_description(),
    long_description_content_type="text/markdown",
    author="ZetxTech",
    author_email="contact@zetxtech.com",
    url="https://github.com/zetxtech/wssocks",
    license="Apache License 2.0",
    
    # Package configuration
    packages=find_packages(include=["wssockslib", "wssockslib.*", "wssocks"]),
    package_data=package_data,
    include_package_data=True,
    
    # Dependencies
    install_requires=install_requires,
    extras_require=extras_require,
    
    # Python version requirement
    python_requires=">=3.7",
    
    # Metadata
    classifiers=[
        "Development Status :: 4 - Beta",
        "Intended Audience :: Developers",
        "Intended Audience :: System Administrators",
        "License :: OSI Approved :: Apache Software License",
        "Operating System :: OS Independent",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.7",
        "Programming Language :: Python :: 3.8",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
        "Programming Language :: Go",
        "Topic :: Internet :: Proxy Servers",
        "Topic :: Internet :: WWW/HTTP",
        "Topic :: Software Development :: Libraries :: Python Modules",
        "Topic :: System :: Networking",
    ],
    keywords="socks proxy websocket network tunneling firewall bypass load-balancing go bindings",
    
    # Entry points
    entry_points={
        "console_scripts": [
            "wssocks=wssocks._cli:cli",
        ],
    },
    
    # Build configuration
    zip_safe=False,  # Due to binary extensions
    platforms=["any"],
    
    # Project URLs
    project_urls={
        "Bug Reports": "https://github.com/zetxtech/wssocks/issues",
        "Source": "https://github.com/zetxtech/wssocks",
        "Documentation": "https://github.com/zetxtech/wssocks#readme",
        "Changelog": "https://github.com/zetxtech/wssocks/releases",
    },
    
    # Binary distribution
    distclass=BinaryDistribution,
)
