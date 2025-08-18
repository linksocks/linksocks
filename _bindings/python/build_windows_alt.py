#!/usr/bin/env python3
"""
Alternative Windows build script for Python 3.13 gopy bindings.

This script uses a different approach - modifying the build environment
to work around gopy's Windows linking issues.
"""

import os
import sys
import subprocess
import shutil
import tempfile
from pathlib import Path

def get_python_info():
    """Get Python version and installation directory."""
    version = f"{sys.version_info.major}{sys.version_info.minor}"
    py_dir = Path(sys.executable).parent
    return version, py_dir

def create_mingw_spec_file(py_version, py_dir):
    """Create a MinGW spec file to handle Python 3.13 linking."""
    if py_version == "313":
        libs_dir = py_dir / "libs"
        python313_lib = libs_dir / "python313.lib"
        
        if python313_lib.exists():
            # Create a temporary directory for our spec file
            temp_dir = Path(tempfile.gettempdir()) / "linksocks_build"
            temp_dir.mkdir(exist_ok=True)
            
            # Create a wrapper script that handles the linking
            wrapper_script = temp_dir / "link_wrapper.sh"
            wrapper_content = f"""#!/bin/bash
# Wrapper script to handle Python 3.13 linking on Windows
args=("$@")
for i in "${{!args[@]}}"; do
    if [[ "${{args[i]}}" == "-lpython313.dll" ]]; then
        args[i]="{python313_lib}"
    fi
done
exec "${{args[@]}}"
"""
            wrapper_script.write_text(wrapper_content)
            
            # Make it executable (if on Unix-like system)
            try:
                wrapper_script.chmod(0o755)
            except:
                pass
            
            return temp_dir
    
    return None

def setup_build_environment(py_version, py_dir):
    """Set up the build environment for Python 3.13."""
    if py_version == "313":
        print(f"Setting up build environment for Python {py_version}")
        
        include_dir = py_dir / "include"
        libs_dir = py_dir / "libs"
        
        # Set basic CGO flags
        if include_dir.exists():
            os.environ["CGO_CFLAGS"] = f"-I{include_dir}"
            print(f"Set CGO_CFLAGS: {os.environ['CGO_CFLAGS']}")
        
        if libs_dir.exists():
            python313_lib = libs_dir / "python313.lib"
            if python313_lib.exists():
                # Try multiple approaches
                
                # Approach 1: Use the Windows-style library path
                lib_path_windows = str(python313_lib).replace('/', '\\')
                os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} \"{lib_path_windows}\""
                print(f"Set CGO_LDFLAGS (Windows style): {os.environ['CGO_LDFLAGS']}")
                
                # Approach 2: Set additional environment variables
                os.environ["LIBRARY_PATH"] = str(libs_dir)
                os.environ["LD_LIBRARY_PATH"] = str(libs_dir)
                
                # Approach 3: Try to use the lib file directly in LDFLAGS
                # This is the most direct approach
                os.environ["CGO_LDFLAGS"] = f"\"{lib_path_windows}\""
                print(f"Set CGO_LDFLAGS (direct): {os.environ['CGO_LDFLAGS']}")
                
                return True
    
    return False

def run_gopy_with_retry():
    """Run gopy build with multiple retry strategies."""
    output_dir = Path("_bindings/python/linksockslib")
    
    # Prepare source files
    orig_file = Path("linksocks/_python.go")
    tmp_file = Path("linksocks/python.go")
    
    try:
        if orig_file.exists():
            shutil.copy2(orig_file, tmp_file)
            print(f"Copied {orig_file} to {tmp_file}")
        
        # Strategy 1: Try with direct library linking
        cmd = [
            "gopy", "build",
            "-vm=python",
            f"-output={output_dir}",
            "-name=linksockslib",
            "-no-make=true",
            "-build-tags=gopy",
            "github.com/zetxtech/linksocks/linksocks"
        ]
        
        print(f"Attempting gopy build...")
        print(f"Command: {' '.join(cmd)}")
        
        # Print environment variables for debugging
        print("Environment variables:")
        for key in ["CGO_CFLAGS", "CGO_LDFLAGS", "CGO_ENABLED", "LIBRARY_PATH", "LD_LIBRARY_PATH"]:
            if key in os.environ:
                print(f"  {key}={os.environ[key]}")
        
        result = subprocess.run(cmd, capture_output=True, text=True, cwd=Path.cwd())
        
        if result.returncode == 0:
            print("Gopy build completed successfully!")
            
            # Create __init__.py
            init_file = output_dir / "__init__.py"
            init_file.write_text("from .linksocks import *\n")
            print(f"Created {init_file}")
            
            return True
        else:
            print(f"Gopy build failed with return code {result.returncode}")
            print(f"STDOUT:\n{result.stdout}")
            print(f"STDERR:\n{result.stderr}")
            
            # If it failed due to linking, try alternative approaches
            if "cannot find -lpython313.dll" in result.stderr:
                print("Detected Python 3.13 .dll linking issue, trying workarounds...")
                return try_linking_workarounds(cmd)
            
            return False
            
    finally:
        # Cleanup
        if tmp_file.exists():
            tmp_file.unlink()
            print(f"Removed {tmp_file}")
        
        # Run go mod tidy
        subprocess.run(["go", "mod", "tidy"], check=False)

def try_linking_workarounds(original_cmd):
    """Try various workarounds for the linking issue."""
    py_version, py_dir = get_python_info()
    libs_dir = py_dir / "libs"
    python313_lib = libs_dir / "python313.lib"
    
    if not python313_lib.exists():
        print("Python 3.13 library not found, cannot apply workarounds")
        return False
    
    # Workaround 1: Create a symbolic .dll file
    python313_dll = libs_dir / "python313.dll"
    created_dll = False
    
    try:
        if not python313_dll.exists():
            # Try to create a hard link first, then copy
            try:
                python313_dll.hardlink_to(python313_lib)
                print(f"Created hard link: {python313_dll} -> {python313_lib}")
                created_dll = True
            except:
                shutil.copy2(python313_lib, python313_dll)
                print(f"Copied {python313_lib} to {python313_dll}")
                created_dll = True
        
        # Reset CGO_LDFLAGS to use standard linking now that .dll exists
        os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython313"
        print(f"Reset CGO_LDFLAGS: {os.environ['CGO_LDFLAGS']}")
        
        # Try the build again
        result = subprocess.run(original_cmd, capture_output=True, text=True)
        
        if result.returncode == 0:
            print("Workaround successful!")
            
            # Create __init__.py
            output_dir = Path("_bindings/python/linksockslib")
            init_file = output_dir / "__init__.py"
            init_file.write_text("from .linksocks import *\n")
            print(f"Created {init_file}")
            
            return True
        else:
            print("Workaround failed")
            print(f"STDERR:\n{result.stderr}")
            return False
            
    finally:
        # Cleanup the created .dll file
        if created_dll and python313_dll.exists():
            try:
                python313_dll.unlink()
                print(f"Cleaned up {python313_dll}")
            except Exception as e:
                print(f"Warning: Could not cleanup {python313_dll}: {e}")
    
    return False

def main():
    """Main build function."""
    print("Alternative Windows Python 3.13 gopy build script")
    print("=" * 50)
    
    # Get Python information
    py_version, py_dir = get_python_info()
    print(f"Python version: {py_version}")
    print(f"Python directory: {py_dir}")
    
    # Set CGO enabled
    os.environ["CGO_ENABLED"] = "1"
    print("Set CGO_ENABLED=1")
    
    # Set up build environment
    env_configured = setup_build_environment(py_version, py_dir)
    
    if py_version == "313" and not env_configured:
        print("Warning: Could not configure Python 3.13 build environment")
    
    # Run gopy build with retry logic
    success = run_gopy_with_retry()
    
    if success:
        print("\n✓ Build completed successfully!")
        return 0
    else:
        print("\n✗ Build failed!")
        return 1

if __name__ == "__main__":
    sys.exit(main())
