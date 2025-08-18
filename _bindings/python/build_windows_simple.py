#!/usr/bin/env python3
"""
Simple Windows build script for Python 3.13 gopy bindings.

This script creates the missing .dll file that gopy expects on Windows.
"""

import os
import sys
import subprocess
import shutil
from pathlib import Path

def main():
    """Main build function."""
    print("Simple Windows Python 3.13 gopy build script")
    print("=" * 45)
    
    # Get Python information
    py_version = f"{sys.version_info.major}{sys.version_info.minor}"
    py_dir = Path(sys.executable).parent
    
    print(f"Python version: {py_version}")
    print(f"Python directory: {py_dir}")
    
    # Set up environment
    os.environ["CGO_ENABLED"] = "1"
    
    if py_version == "313":
        print("Configuring for Python 3.13...")
        
        include_dir = py_dir / "include"
        libs_dir = py_dir / "libs"
        
        # Set CGO flags
        if include_dir.exists():
            os.environ["CGO_CFLAGS"] = f"-I{include_dir}"
            print(f"Set CGO_CFLAGS: {os.environ['CGO_CFLAGS']}")
        
        if libs_dir.exists():
            # The key insight: create python313.dll from python313.lib
            python313_lib = libs_dir / "python313.lib"
            python313_dll = libs_dir / "python313.dll"
            
            if python313_lib.exists() and not python313_dll.exists():
                try:
                    # Copy .lib to .dll - this is what the linker expects
                    shutil.copy2(python313_lib, python313_dll)
                    print(f"✓ Created {python313_dll}")
                    
                    # Now use standard linking
                    os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython313"
                    print(f"Set CGO_LDFLAGS: {os.environ['CGO_LDFLAGS']}")
                    
                except Exception as e:
                    print(f"Warning: Could not create .dll file: {e}")
                    return 1
            elif python313_dll.exists():
                print(f"✓ Found existing {python313_dll}")
                os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython313"
                print(f"Set CGO_LDFLAGS: {os.environ['CGO_LDFLAGS']}")
            else:
                print(f"Error: Python library not found: {python313_lib}")
                return 1
    
    # Prepare source files
    orig_file = Path("linksocks/_python.go")
    tmp_file = Path("linksocks/python.go")
    output_dir = Path("_bindings/python/linksockslib")
    
    created_dll = None
    
    try:
        if orig_file.exists():
            shutil.copy2(orig_file, tmp_file)
            print(f"Copied {orig_file} to {tmp_file}")
        
        # Run gopy build
        cmd = [
            "gopy", "build",
            "-vm=python",
            f"-output={output_dir}",
            "-name=linksockslib",
            "-no-make=true",
            "-build-tags=gopy",
            "github.com/zetxtech/linksocks/linksocks"
        ]
        
        print(f"Running: {' '.join(cmd)}")
        result = subprocess.run(cmd, capture_output=True, text=True)
        
        if result.returncode == 0:
            print("✓ Gopy build completed successfully!")
            
            # Create __init__.py
            init_file = output_dir / "__init__.py"
            init_file.write_text("from .linksocks import *\n")
            print(f"✓ Created {init_file}")
            
            return 0
        else:
            print(f"✗ Gopy build failed with return code {result.returncode}")
            if result.stdout:
                print(f"STDOUT:\n{result.stdout}")
            if result.stderr:
                print(f"STDERR:\n{result.stderr}")
            return 1
            
    finally:
        # Cleanup
        if tmp_file.exists():
            tmp_file.unlink()
            print(f"Cleaned up {tmp_file}")
        
        # Clean up created .dll if it was temporary
        if py_version == "313" and created_dll and created_dll.exists():
            try:
                created_dll.unlink()
                print(f"Cleaned up {created_dll}")
            except:
                pass
        
        # Run go mod tidy
        subprocess.run(["go", "mod", "tidy"], check=False)

if __name__ == "__main__":
    sys.exit(main())
