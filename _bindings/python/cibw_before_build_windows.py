#!/usr/bin/env python3
"""
Cibuildwheel before-build hook for Windows Python 3.13.

This script configures the environment for building Python wheels on Windows,
specifically handling Python 3.13 linking issues.
"""

import os
import sys
from pathlib import Path

def main():
    """Configure environment for wheel building."""
    py_version = f"{sys.version_info.major}{sys.version_info.minor}"
    py_dir = Path(sys.executable).parent
    
    print(f"Cibuildwheel Windows build hook - Python {py_version}")
    print(f"Python directory: {py_dir}")
    
    # Special configuration for Python 3.13
    if py_version == "313":
        print("Applying Python 3.13 Windows build configuration...")
        
        # Set up CGO flags for proper linking
        include_dir = py_dir / "include"
        libs_dir = py_dir / "libs"
        
        cgo_cflags = ""
        cgo_ldflags = ""
        
        if include_dir.exists():
            cgo_cflags = f"-I{include_dir}"
            print(f"Include directory found: {include_dir}")
        else:
            print(f"Warning: Include directory not found: {include_dir}")
        
        if libs_dir.exists():
            # Check for Python library files
            lib_files = list(libs_dir.glob("python*.lib"))
            if lib_files:
                print(f"Python libraries found: {[f.name for f in lib_files]}")
                cgo_ldflags = f"-L{libs_dir} -lpython{py_version}"
            else:
                print(f"Warning: No Python library files in {libs_dir}")
        else:
            print(f"Warning: Libs directory not found: {libs_dir}")
        
        # Set environment variables for the build process
        if cgo_cflags:
            os.environ["CGO_CFLAGS"] = cgo_cflags
            print(f"Set CGO_CFLAGS={cgo_cflags}")
        
        if cgo_ldflags:
            os.environ["CGO_LDFLAGS"] = cgo_ldflags
            print(f"Set CGO_LDFLAGS={cgo_ldflags}")
    
    # Ensure CGO is enabled
    os.environ["CGO_ENABLED"] = "1"
    print("Set CGO_ENABLED=1")
    
    print("Environment configuration completed")

if __name__ == "__main__":
    main()
