#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import os
import sys
import subprocess
import shutil
from pathlib import Path

# Ensure UTF-8 encoding for Windows compatibility
if sys.platform == "win32":
    import io
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8', errors='replace')
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding='utf-8', errors='replace')

def main():
    print("Simple Windows Python 3.13 gopy build script")
    print("=" * 45)
    
    py_version = f"{sys.version_info.major}{sys.version_info.minor}"
    py_dir = Path(sys.executable).parent
    
    print(f"Python version: {py_version}")
    print(f"Python directory: {py_dir}")
    
    os.environ["CGO_ENABLED"] = "1"
    
    # Configure environment for all Python versions
    include_dir = py_dir / "include"
    libs_dir = py_dir / "libs"
    
    if include_dir.exists():
        os.environ["CGO_CFLAGS"] = f"-I{include_dir}"
        print(f"Set CGO_CFLAGS: {os.environ['CGO_CFLAGS']}")
    
    if py_version == "313":
        print("Applying Python 3.13 specific configuration...")
        
        if libs_dir.exists():
            python313_lib = libs_dir / "python313.lib"
            python313_dll = libs_dir / "python313.dll"
            
            if python313_lib.exists() and not python313_dll.exists():
                try:
                    shutil.copy2(python313_lib, python313_dll)
                    print(f"[OK] Created {python313_dll}")
                    
                    os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython313"
                    print(f"Set CGO_LDFLAGS: {os.environ['CGO_LDFLAGS']}")
                    
                except Exception as e:
                    print(f"Warning: Could not create .dll file: {e}")
                    return 1
            elif python313_dll.exists():
                print(f"[OK] Found existing {python313_dll}")
                os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython313"
                print(f"Set CGO_LDFLAGS: {os.environ['CGO_LDFLAGS']}")
            else:
                print(f"Error: Python library not found: {python313_lib}")
                return 1
        else:
            print(f"Error: Libs directory not found: {libs_dir}")
            return 1
    else:
        print(f"Python {py_version} - using standard configuration")
        if libs_dir.exists():
            os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython{py_version}"
            print(f"Set CGO_LDFLAGS: {os.environ['CGO_LDFLAGS']}")
    
    orig_file = Path("linksocks/_python.go")
    tmp_file = Path("linksocks/python.go")
    output_dir = Path("_bindings/python/linksockslib")
    
    created_dll = None
    
    try:
        if orig_file.exists():
            shutil.copy2(orig_file, tmp_file)
            print(f"Copied {orig_file} to {tmp_file}")
        
        cmd = [
            "gopy", "build",
            "-vm=python",
            f"-output={output_dir}",
            "-name=linksockslib",
            "-no-make=true",
            "-build-tags=gopy",
            "github.com/linksocks/linksocks/linksocks"
        ]
        
        print(f"Running: {' '.join(cmd)}")
        result = subprocess.run(cmd, capture_output=True, text=True)
        
        if result.returncode == 0:
            print("[SUCCESS] Gopy build completed successfully!")
            
            init_file = output_dir / "__init__.py"
            init_file.write_text("from .linksocks import *\n")
            print(f"[OK] Created {init_file}")
            
            return 0
        else:
            print(f"[ERROR] Gopy build failed with return code {result.returncode}")
            if result.stdout:
                print(f"STDOUT:\n{result.stdout}")
            if result.stderr:
                print(f"STDERR:\n{result.stderr}")
            return 1
            
    finally:
        if tmp_file.exists():
            tmp_file.unlink()
            print(f"Cleaned up {tmp_file}")
        
        if py_version == "313" and created_dll and created_dll.exists():
            try:
                created_dll.unlink()
                print(f"Cleaned up {created_dll}")
            except:
                pass
        
        subprocess.run(["go", "mod", "tidy"], check=False)

if __name__ == "__main__":
    sys.exit(main())
