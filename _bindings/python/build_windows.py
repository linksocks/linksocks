#!/usr/bin/env python3
"""
Windows-specific build script for Python 3.13 gopy bindings.

This script handles the special linking requirements for Python 3.13 on Windows.
"""

import os
import sys
import subprocess
import shutil
from pathlib import Path

def get_python_info():
    """Get Python version and installation directory."""
    version = f"{sys.version_info.major}{sys.version_info.minor}"
    py_dir = Path(sys.executable).parent
    return version, py_dir

def configure_python313_environment(py_version, py_dir):
    """Configure environment variables for Python 3.13 linking."""
    if py_version == "313":
        print(f"Configuring environment for Python {py_version}")
        
        # Set CGO flags for Python 3.13
        include_dir = py_dir / "include"
        libs_dir = py_dir / "libs"
        
        if include_dir.exists():
            os.environ["CGO_CFLAGS"] = f"-I{include_dir}"
            print(f"Set CGO_CFLAGS: {os.environ['CGO_CFLAGS']}")
        else:
            print(f"Warning: Include directory not found: {include_dir}")
        
        if libs_dir.exists():
            # Check for available Python library files
            lib_files = list(libs_dir.glob("python*.lib"))
            if lib_files:
                print(f"Available Python libraries: {[f.name for f in lib_files]}")
                
                # Workaround for gopy's .dll assumption on Windows
                # Create a .dll file that points to the .lib file
                python313_lib = libs_dir / "python313.lib"
                python313_dll = libs_dir / "python313.dll"
                
                if python313_lib.exists():
                    try:
                        # Method 1: Try to create a .dll file from .lib
                        if not python313_dll.exists():
                            shutil.copy2(python313_lib, python313_dll)
                            print(f"Created {python313_dll} from {python313_lib}")
                        
                        # Method 2: Use direct library path instead of -l flag
                        # This bypasses gopy's .dll assumption
                        lib_path = str(python313_lib).replace('\\', '/')
                        os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} {lib_path}"
                        print(f"Set CGO_LDFLAGS (direct path): {os.environ['CGO_LDFLAGS']}")
                        
                        # Also try setting LIBRARY_PATH for extra compatibility
                        os.environ["LIBRARY_PATH"] = str(libs_dir)
                        print(f"Set LIBRARY_PATH: {os.environ['LIBRARY_PATH']}")
                        
                        return python313_dll  # Return the created .dll file for cleanup
                        
                    except Exception as e:
                        print(f"Warning: Could not create .dll file: {e}")
                        # Fallback to using the full path only
                        lib_path = str(python313_lib).replace('\\', '/')
                        os.environ["CGO_LDFLAGS"] = f"{lib_path}"
                        print(f"Set CGO_LDFLAGS (fallback direct): {os.environ['CGO_LDFLAGS']}")
                else:
                    # Fallback to standard linking
                    os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython{py_version}"
                    print(f"Set CGO_LDFLAGS (standard fallback): {os.environ['CGO_LDFLAGS']}")
            else:
                print(f"Warning: No Python library files found in {libs_dir}")
        else:
            print(f"Warning: Libs directory not found: {libs_dir}")
    else:
        print(f"Python version {py_version} - no special configuration needed")
    
    return None

def run_gopy_build():
    """Run gopy build command with proper error handling."""
    output_dir = Path("_bindings/python/linksockslib")
    
    # Prepare source files
    orig_file = Path("linksocks/_python.go")
    tmp_file = Path("linksocks/python.go")
    
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
            print("Gopy build completed successfully")
            
            # Create __init__.py
            init_file = output_dir / "__init__.py"
            init_file.write_text("from .linksocks import *\n")
            print(f"Created {init_file}")
            
        else:
            print(f"Gopy build failed with return code {result.returncode}")
            print(f"STDOUT: {result.stdout}")
            print(f"STDERR: {result.stderr}")
            return False
            
    finally:
        # Cleanup
        if tmp_file.exists():
            tmp_file.unlink()
            print(f"Removed {tmp_file}")
        
        # Run go mod tidy
        subprocess.run(["go", "mod", "tidy"], check=False)
    
    return result.returncode == 0

def main():
    """Main build function."""
    print("Windows Python 3.13 gopy build script")
    print("=" * 40)
    
    # Get Python information
    py_version, py_dir = get_python_info()
    print(f"Python version: {py_version}")
    print(f"Python directory: {py_dir}")
    
    # Configure environment for Python 3.13
    created_dll = configure_python313_environment(py_version, py_dir)
    
    # Set CGO enabled
    os.environ["CGO_ENABLED"] = "1"
    print("Set CGO_ENABLED=1")
    
    try:
        # Run gopy build
        success = run_gopy_build()
        
        if success:
            print("\nBuild completed successfully!")
            return 0
        else:
            print("\nBuild failed!")
            return 1
    finally:
        # Cleanup created .dll file if it exists
        if created_dll and created_dll.exists():
            try:
                created_dll.unlink()
                print(f"Cleaned up {created_dll}")
            except Exception as e:
                print(f"Warning: Could not cleanup {created_dll}: {e}")

if __name__ == "__main__":
    sys.exit(main())
