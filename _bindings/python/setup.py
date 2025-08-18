#!/usr/bin/env python3
"""
Setup script for linksocks Python bindings.

LinkSocks is a SOCKS proxy implementation over WebSocket protocol.
This package provides Python bindings for the Go implementation.
"""

import os
import sys
import shutil
import subprocess
import platform
import tempfile
import tarfile
import zipfile
from pathlib import Path
from setuptools import setup, find_packages
import setuptools
from urllib.request import urlretrieve
from setuptools.command.sdist import sdist as _sdist
from setuptools.command.build_py import build_py as _build_py
from setuptools.command.develop import develop as _develop
from setuptools.command.install import install as _install

# Get the current directory
here = Path(__file__).parent.absolute()

def prepare_go_sources():
    """Prepare Go source files by copying them to linksocks_go directory."""
    go_src_dir = here / "linksocks_go"
    
    # If linksocks_go already exists (e.g., from source distribution), use it
    if go_src_dir.exists() and (go_src_dir / "_python.go").exists():
        print(f"Using existing Go sources in {go_src_dir}")
        return go_src_dir
    
    print("Preparing Go source files...")
    
    # Try to find project root (go up from _bindings/python/)
    project_root = here.parent.parent
    if not (project_root / "go.mod").exists():
        raise FileNotFoundError("Cannot find project root with go.mod file")
    
    # Create linksocks_go directory
    if go_src_dir.exists():
        shutil.rmtree(go_src_dir)
    go_src_dir.mkdir()
    
    # Copy go.mod and go.sum to parent directory (here)
    for file in ["go.mod", "go.sum"]:
        src = project_root / file
        if src.exists():
            shutil.copy2(src, here / file)
            print(f"Copied {file} to {here}")
    
    # Copy linksocks Go files to linksocks_go directory
    linksocks_src = project_root / "linksocks"
    if linksocks_src.exists():
        for go_file in linksocks_src.glob("*.go"):
            shutil.copy2(go_file, go_src_dir / go_file.name)
            print(f"Copied {go_file.name} to linksocks_go/")
    else:
        raise FileNotFoundError("Cannot find linksocks source directory")
    
    print(f"Go sources prepared in {go_src_dir}")
    return go_src_dir

def run_command(cmd, cwd=None, env=None):
    """Run a command and return the result."""
    print(f"Running: {' '.join(cmd)}")
    try:
        # Use current environment if no env is provided
        if env is None:
            env = os.environ.copy()
        result = subprocess.run(
            cmd, 
            cwd=cwd, 
            env=env, 
            capture_output=True, 
            text=True, 
            check=True
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        print(f"Command failed: {e}")
        print(f"stdout: {e.stdout}")
        print(f"stderr: {e.stderr}")
        raise

def check_go_installation():
    """Check if Go is installed and return version."""
    try:
        result = run_command(["go", "version"])
        print(f"Found Go: {result}")
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False

def download_file(url, destination):
    """Download a file from URL to destination."""
    print(f"Downloading {url} to {destination}")
    urlretrieve(url, destination)

# Global variable to store temporary Go installation
_temp_go_dir = None

def install_go():
    """Download and install Go if not available."""
    global _temp_go_dir
    
    if check_go_installation():
        return
    
    print("Go not found, downloading and installing to temporary directory...")
    
    # Determine platform and architecture
    system = platform.system().lower()
    machine = platform.machine().lower()
    
    # Map architecture names
    arch_map = {
        'x86_64': 'amd64',
        'amd64': 'amd64',
        'i386': '386',
        'i686': '386',
        'arm64': 'arm64',
        'aarch64': 'arm64',
    }
    
    arch = arch_map.get(machine, 'amd64')
    go_version = "1.21.6"
    
    if system == "windows":
        go_filename = f"go{go_version}.windows-{arch}.zip"
    elif system == "darwin":
        go_filename = f"go{go_version}.darwin-{arch}.tar.gz"
    else:  # linux
        go_filename = f"go{go_version}.linux-{arch}.tar.gz"
    go_url = f"https://dl.google.com/go/{go_filename}"
    
    # Create temporary directory for Go installation (don't delete it yet)
    _temp_go_dir = tempfile.mkdtemp(prefix="go_install_")
    temp_dir_path = Path(_temp_go_dir)
    
    try:
        go_archive = temp_dir_path / go_filename
        download_file(go_url, go_archive)
        
        print(f"Installing Go to temporary directory: {temp_dir_path}")
        
        # Extract Go to temporary directory
        if system == "windows":
            with zipfile.ZipFile(go_archive, 'r') as zip_ref:
                zip_ref.extractall(temp_dir_path)
        else:
            with tarfile.open(go_archive, 'r:gz') as tar_ref:
                tar_ref.extractall(temp_dir_path)
        
        # Go is extracted to temp_dir/go/
        go_root = temp_dir_path / "go"
        go_bin = go_root / "bin"
        
        # Update PATH
        current_path = os.environ.get("PATH", "")
        if str(go_bin) not in current_path:
            os.environ["PATH"] = f"{go_bin}{os.pathsep}{current_path}"
        
        print(f"Updated PATH to include Go: {go_bin}")
        
        # Set GOROOT
        os.environ["GOROOT"] = str(go_root)
        
        # Set GOPATH and GOMODCACHE to temporary locations
        go_workspace = temp_dir_path / "go-workspace"
        os.environ["GOPATH"] = str(go_workspace)
        os.environ["GOMODCACHE"] = str(go_workspace / "pkg" / "mod")
        
        # Create directories if they don't exist
        go_workspace.mkdir(exist_ok=True)
        (go_workspace / "pkg" / "mod").mkdir(parents=True, exist_ok=True)
        
        print(f"Go installed successfully to temporary directory: {go_root}")
        
    except Exception as e:
        # Clean up on error
        if _temp_go_dir and Path(_temp_go_dir).exists():
            shutil.rmtree(_temp_go_dir)
            _temp_go_dir = None
        raise e

def cleanup_temp_go():
    """Clean up temporary Go installation."""
    global _temp_go_dir
    if _temp_go_dir and Path(_temp_go_dir).exists():
        print(f"Cleaning up temporary Go installation: {_temp_go_dir}")
        try:
            # Try to make files writable before deletion
            import stat
            for root, dirs, files in os.walk(_temp_go_dir):
                for d in dirs:
                    os.chmod(os.path.join(root, d), stat.S_IRWXU | stat.S_IRWXG | stat.S_IRWXO)
                for f in files:
                    os.chmod(os.path.join(root, f), stat.S_IRWXU | stat.S_IRWXG | stat.S_IRWXO)
            shutil.rmtree(_temp_go_dir)
            _temp_go_dir = None
        except Exception as e:
            print(f"Warning: Failed to clean up temporary Go installation: {e}")
            _temp_go_dir = None

def install_gopy_and_tools():
    """Install gopy and related Go tools."""
    print("Installing gopy and Go tools...")
    
    # Ensure Go is available
    if not check_go_installation():
        raise RuntimeError("Go is not available after installation attempt")
    
    # Install gopy
    try:
        run_command(["go", "install", "github.com/go-python/gopy@latest"])
        print("gopy installed successfully")
    except subprocess.CalledProcessError as e:
        print(f"Failed to install gopy: {e}")
        raise
    
    # Install goimports
    try:
        run_command(["go", "install", "golang.org/x/tools/cmd/goimports@latest"])
        print("goimports installed successfully")
    except subprocess.CalledProcessError as e:
        print(f"Failed to install goimports: {e}")
        raise

def build_python_bindings():
    """Build Python bindings using gopy."""
    print("Building Python bindings with gopy...")
    
    # Prepare Go sources first
    go_src_dir = prepare_go_sources()
    temp_file = None
    
    try:
        # Clean existing bindings
        linksocks_lib_dir = here / "linksockslib"
        if linksocks_lib_dir.exists():
            shutil.rmtree(linksocks_lib_dir)
            print(f"Cleaned existing {linksocks_lib_dir}")
        
        # Copy _python.go to python.go if it exists
        orig_file = go_src_dir / "_python.go"
        temp_file = go_src_dir / "python.go"
        
        if orig_file.exists():
            shutil.copy2(orig_file, temp_file)
            print(f"Copied {orig_file} to {temp_file}")
        
        # Set up environment
        env = os.environ.copy()
        env["CGO_ENABLED"] = "1"
        
        # Ensure PATH includes Go binary directory
        if _temp_go_dir:
            go_bin = Path(_temp_go_dir) / "go" / "bin"
            current_path = env.get("PATH", "")
            if str(go_bin) not in current_path:
                env["PATH"] = f"{go_bin}{os.pathsep}{current_path}"
                print(f"Updated PATH for gopy execution: {go_bin}")
        
        # Run gopy build from _bindings/python directory
        cmd = [
            "gopy", "build",
            f"-vm={sys.executable}",
            f"-output={linksocks_lib_dir}",
            "-name=linksockslib",
            "-no-make=true",
            "-build-tags=gopy",
            "./linksocks_go"  # Use linksocks_go directory
        ]
        
        run_command(cmd, cwd=here, env=env)
        
        # Create __init__.py
        init_file = linksocks_lib_dir / "__init__.py"
        with open(init_file, "w") as f:
            f.write("from .linksocks import *\n")
        print(f"Created {init_file}")
        
        # Clean up go.mod
        run_command(["go", "mod", "tidy"], cwd=here)
        
        print("Python bindings built successfully")
        
    finally:
        # Clean up temporary python.go file
        if temp_file and temp_file.exists():
            temp_file.unlink()
            print(f"Cleaned up {temp_file}")
        
        # Clean up linksocks_go directory and go.mod/go.sum
        if go_src_dir.exists():
            shutil.rmtree(go_src_dir)
            print(f"Cleaned up {go_src_dir}")
        
        for file in ["go.mod", "go.sum"]:
            temp_go_file = here / file
            if temp_go_file.exists():
                temp_go_file.unlink()
                print(f"Cleaned up {temp_go_file}")

def ensure_python_bindings():
    """Ensure Python bindings are available, build if necessary."""
    linksocks_lib_dir = here / "linksockslib"
    local_go_src_dir = here / "linksocks_go"
    local_go_mod = here / "go.mod"
    
    # Check if we're in a wheel build environment (skip building in this case)
    if os.environ.get('CIBUILDWHEEL', '0') == '1':
        print("Running in cibuildwheel environment, bindings should be pre-built")
        return
    
    if not linksocks_lib_dir.exists():
        print("linksockslib directory not found, building Python bindings...")
        
        # Determine availability of Go sources
        have_local_sources = local_go_src_dir.exists() and (local_go_src_dir / "_python.go").exists() and local_go_mod.exists()
        if not have_local_sources:
            # Fallback to project root layout (building from repo)
            try:
                project_root = here.parent.parent
                if not (project_root / "go.mod").exists():
                    raise FileNotFoundError("Cannot find project root with go.mod file")
            except Exception:
                raise RuntimeError(
                    "Cannot find Go source files. "
                    "This package should be built from the linksocks source repository, "
                    "or you should use a pre-built wheel."
                )
        
        # Check if we have Go available
        if not check_go_installation():
            print("Go not found, attempting to install...")
            try:
                install_go()
            except Exception as e:
                print(f"Failed to install Go: {e}")
                raise RuntimeError(
                    "Go is required to build linksocks from source. "
                    "Please install Go 1.21+ from https://golang.org/dl/ or use a pre-built wheel."
                )
        
        try:
            # Install gopy and tools
            install_gopy_and_tools()
            
            # Install Python dependencies for building
            try:
                run_command([sys.executable, "-m", "pip", "install", "pybindgen", "wheel", "setuptools"])
            except subprocess.CalledProcessError:
                print("Warning: Failed to install some Python build dependencies")
            
            # Build bindings
            build_python_bindings()
            
        except Exception as e:
            print(f"Failed to build Python bindings: {e}")
            raise RuntimeError(
                f"Failed to build linksocks from source: {e}\n"
                "This may be due to missing dependencies or incompatible system.\n"
                "Try installing a pre-built wheel or ensure Go 1.21+ is installed."
            )
        finally:
            # Clean up temporary Go installation
            cleanup_temp_go()
        
        if not linksocks_lib_dir.exists():
            raise RuntimeError("Failed to build Python bindings")
    else:
        print(f"Found existing linksockslib directory at {linksocks_lib_dir}")

def test_bindings():
    """Test if the Python bindings work correctly."""
    try:
        # Try to import the bindings
        sys.path.insert(0, str(here))
        import linksockslib
        print("✓ Python bindings imported successfully")
        
        # Try to access some basic functionality
        if hasattr(linksockslib, '__version__') or hasattr(linksockslib, 'NewClient'):
            print("✓ Python bindings appear to be functional")
        else:
            print("⚠ Python bindings imported but may not be fully functional")
        
        return True
    except ImportError as e:
        print(f"✗ Failed to import Python bindings: {e}")
        return False
    except Exception as e:
        print(f"✗ Error testing Python bindings: {e}")
        return False
    finally:
        # Clean up sys.path
        if str(here) in sys.path:
            sys.path.remove(str(here))

# Read description from README
def get_long_description():
    """Get long description from README file."""
    # Use local README
    local_readme = here / "README.md"
    if local_readme.exists():
        with open(local_readme, "r", encoding="utf-8") as f:
            return f.read()
    else:
        # Fallback to a simple description
        return "Python bindings for LinkSocks - a SOCKS proxy implementation over WebSocket protocol."

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
        "pytest-xdist",
        "black>=21.0",
        "flake8>=3.8",
        "mypy>=0.800",
        "httpx[socks]",
        "requests",
        "pysocks",
    ],
}

class SdistWithGoSources(_sdist):
    """Custom sdist that ensures Go sources (linksocks_go, go.mod, go.sum) exist
    before creating the source distribution, and cleans them afterwards.
    """

    def run(self):
        go_src_dir = None
        created_files = []
        try:
            go_src_dir = prepare_go_sources()
            # Track go.mod and go.sum created in this directory for cleanup
            for fname in ["go.mod", "go.sum"]:
                fpath = here / fname
                if fpath.exists():
                    created_files.append(fpath)
            super().run()
        finally:
            # Clean up generated Go sources and module files after sdist
            try:
                if go_src_dir and Path(go_src_dir).exists():
                    shutil.rmtree(go_src_dir)
                    print(f"Cleaned up {go_src_dir}")
            except Exception as cleanup_err:
                print(f"Warning: failed to remove {go_src_dir}: {cleanup_err}")
            for fpath in created_files:
                try:
                    if fpath.exists():
                        fpath.unlink()
                        print(f"Cleaned up {fpath}")
                except Exception as cleanup_err:
                    print(f"Warning: failed to remove {fpath}: {cleanup_err}")


class BuildPyEnsureBindings(_build_py):
    """Ensure Python bindings exist when building the package (wheel/install).

    This avoids heavy work at import time and only triggers during actual builds.
    """

    def run(self):
        try:
            ensure_python_bindings()
        except Exception as e:
            # Do not fail metadata-only operations; re-raise for real builds
            if os.environ.get("SETUPTOOLS_BUILD_META", ""):  # PEP 517 builds
                raise
            raise
        super().run()


class DevelopEnsureBindings(_develop):
    """Ensure bindings exist for editable installs (pip install -e .)."""

    def run(self):
        ensure_python_bindings()
        super().run()


class InstallEnsureBindings(_install):
    """Ensure bindings exist for regular installs (pip install .)."""

    def run(self):
        ensure_python_bindings()
        super().run()

class BinaryDistribution(setuptools.Distribution):
    def has_ext_modules(_):
        return True

def configure_windows_python_env():
    """Configure CGO flags for Windows across supported Python versions."""
    import platform
    import sys
    from pathlib import Path

    if platform.system() == "Windows":
        py_version = f"{sys.version_info.major}{sys.version_info.minor}"
        py_dir = Path(sys.executable).parent
        include_dir = py_dir / "include"
        libs_dir = py_dir / "libs"

        os.environ["CGO_ENABLED"] = "1"

        if include_dir.exists():
            os.environ["CGO_CFLAGS"] = f"-I{include_dir}"
            print(f"Configured CGO_CFLAGS for Windows: {os.environ['CGO_CFLAGS']}")

        if libs_dir.exists():
            os.environ["CGO_LDFLAGS"] = f"-L{libs_dir} -lpython{py_version}"
            print(f"Configured CGO_LDFLAGS for Windows: {os.environ['CGO_LDFLAGS']}")

# Configure Windows Python environment if needed
configure_windows_python_env()

# Test bindings if requested via environment variable
if os.environ.get('LINKSOCKS_TEST_BINDINGS', '').lower() in ('1', 'true', 'yes'):
    print("\nTesting Python bindings...")
    test_bindings()

setup(
    name="linksocks",
    version="3.0.3",
    description="Python bindings for LinkSocks - SOCKS proxy over WebSocket",
    long_description=get_long_description(),
    long_description_content_type="text/markdown",
    author="jackzzs",
    author_email="jackzzs@outlook.com",
    url="https://github.com/zetxtech/linksocks",
    license="MIT",
    
    # Package configuration
    packages=find_packages(include=["linksockslib", "linksockslib.*", "linksocks"]),
    package_data={
        # Include native artifacts and helper sources generated by gopy
        "linksockslib": ["*.py", "*.so", "*.pyd", "*.dll", "*.dylib", "*.h", "*.c", "*.go"],
    },
    include_package_data=True,
    
    # Dependencies
    install_requires=install_requires,
    extras_require=extras_require,
    
    # Python version requirement
    python_requires=">=3.9",
    
    # Metadata
    classifiers=[
        "Development Status :: 4 - Beta",
        "Intended Audience :: Developers",
        "Intended Audience :: System Administrators",
        "License :: OSI Approved :: MIT License",
        "Operating System :: POSIX :: Linux",
        "Operating System :: MacOS",
        "Operating System :: Microsoft :: Windows",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
        "Programming Language :: Python :: 3.13",
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
            "linksocks=linksocks._cli:cli",
        ],
    },
    
    # Build configuration
    zip_safe=False,  # Due to binary extensions
    platforms=["any"],
    
    # Project URLs
    project_urls={
        "Bug Reports": "https://github.com/zetxtech/linksocks/issues",
        "Source": "https://github.com/zetxtech/linksocks",
        "Documentation": "https://github.com/zetxtech/linksocks#readme",
        "Changelog": "https://github.com/zetxtech/linksocks/releases",
    },
    
    # Binary distribution
    distclass=BinaryDistribution,
    cmdclass={
        "sdist": SdistWithGoSources,
        "build_py": BuildPyEnsureBindings,
        "develop": DevelopEnsureBindings,
        "install": InstallEnsureBindings,
    },
)
