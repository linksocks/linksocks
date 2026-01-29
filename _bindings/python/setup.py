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
from typing import Optional

# Get the current directory
here = Path(__file__).parent.absolute()

# Global variables
_temp_go_dir = None
_temp_py_venv_dir = None

# Ensure Go builds do not require VCS (git) metadata; avoids errors on minimal images
current_goflags = os.environ.get("GOFLAGS", "").strip()
if "-buildvcs=false" not in current_goflags:
    os.environ["GOFLAGS"] = (current_goflags + (" " if current_goflags else "") + "-buildvcs=false").strip()

# Platform-specific configurations
install_requires = [
    "setuptools>=40.0",
    "click>=8.0",
    "loguru",
    "rich",
    "cffi>=1.15",
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


def prepare_ffi_go_sources() -> Path:
    """Ensure the Go FFI shim sources exist in linksocks_go_ffi directory."""
    ffi_src_dir = here / "linksocks_go_ffi"
    if ffi_src_dir.exists() and any(ffi_src_dir.glob("*.go")):
        return ffi_src_dir

    project_root = here.parent.parent
    candidate = project_root / "_bindings" / "python" / "linksocks_go_ffi"
    if candidate.exists() and any(candidate.glob("*.go")):
        if ffi_src_dir.exists():
            shutil.rmtree(ffi_src_dir)
        shutil.copytree(candidate, ffi_src_dir)
        return ffi_src_dir

    raise FileNotFoundError("Cannot find linksocks_go_ffi sources")


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

# (globals moved to top)

def _venv_scripts_dir(venv_dir: Path) -> Path:
    system = platform.system().lower()
    if system == "windows":
        return venv_dir / "Scripts"
    return venv_dir / "bin"

def create_temp_virtualenv() -> tuple[Path, Path]:
    """Create a temporary Python virtual environment and return (venv_dir, python_exe)."""
    global _temp_py_venv_dir
    _temp_py_venv_dir = Path(tempfile.mkdtemp(prefix="linksocks_pyvenv_"))
    venv_dir = _temp_py_venv_dir / "venv"
    try:
        # Prefer stdlib venv
        run_command([sys.executable, "-m", "venv", str(venv_dir)])
    except Exception:
        # Fallback: install and use virtualenv from PyPI
        try:
            pip_cmd = get_pip_invocation()
            run_command(pip_cmd + ["install", "--upgrade", "pip"])
            run_command(pip_cmd + ["install", "virtualenv"])
            run_command([sys.executable, "-m", "virtualenv", str(venv_dir)])
        except Exception as e:
            raise RuntimeError(
                f"Failed to create a virtual environment using both venv and virtualenv: {e}"
            )
    scripts_dir = _venv_scripts_dir(venv_dir)
    python_exe = scripts_dir / ("python.exe" if platform.system().lower() == "windows" else "python")
    return venv_dir, python_exe

def ensure_pip_for_python(python_executable: Path) -> list[str]:
    """Ensure pip is available for the given Python interpreter and return invocation list.

    It tries `-m pip`, bootstraps via `ensurepip` if necessary, and falls back to pip executables
    within the venv's scripts directory if present.
    """
    # Try module form
    try:
        run_command([str(python_executable), "-m", "pip", "--version"])
        return [str(python_executable), "-m", "pip"]
    except Exception:
        pass

    # Bootstrap via ensurepip
    try:
        run_command([str(python_executable), "-m", "ensurepip", "--upgrade"])
        run_command([str(python_executable), "-m", "pip", "--version"])
        return [str(python_executable), "-m", "pip"]
    except Exception:
        pass

    # Fallback to pip executable in the same venv
    scripts_dir = Path(python_executable).parent
    for candidate in ("pip3", "pip"):
        pip_exe = scripts_dir / (candidate + (".exe" if platform.system().lower() == "windows" else ""))
        if pip_exe.exists():
            try:
                run_command([str(pip_exe), "--version"])
                return [str(pip_exe)]
            except Exception:
                continue

    raise RuntimeError(
        f"pip is not available for interpreter {python_executable} and could not be bootstrapped via ensurepip."
    )

def get_pip_invocation() -> list[str]:
    """Return a command list to invoke pip reliably in diverse environments.

    Strategy:
    1) Prefer `sys.executable -m pip` if available
    2) If missing, bootstrap via `ensurepip` and retry
    3) Fallback to a `pip` executable on PATH (pip3, then pip)
    """
    # 1) Try module form first
    try:
        run_command([sys.executable, "-m", "pip", "--version"])
        return [sys.executable, "-m", "pip"]
    except Exception:
        pass

    # 2) Try bootstrapping pip via ensurepip
    try:
        run_command([sys.executable, "-m", "ensurepip", "--upgrade"])
        run_command([sys.executable, "-m", "pip", "--version"])
        return [sys.executable, "-m", "pip"]
    except Exception:
        pass

    # 3) Fallback to a pip executable on PATH
    for candidate in ("pip3", "pip"):
        pip_exe = shutil.which(candidate)
        if pip_exe:
            try:
                run_command([pip_exe, "--version"])
                return [pip_exe]
            except Exception:
                continue

    raise RuntimeError(
        "pip is not available and could not be bootstrapped via ensurepip. "
        "Please ensure pip is installed for this Python interpreter."
    )

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
                try:
                    # Python 3.12+ supports the 'filter' argument; Python 3.14 defaults to filtering.
                    # Use 'data' for safety and cross-version consistency; fall back if unsupported.
                    tar_ref.extractall(temp_dir_path, filter='data')
                except TypeError:
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

def ensure_python_bindings():
    """Ensure the C-ABI FFI shared library is available, build if necessary."""
    ffi_dir = here / "linksocks_ffi"
    ffi_candidates = [
        ffi_dir / "liblinksocks_ffi.so",
        ffi_dir / "liblinksocks_ffi.dylib",
        ffi_dir / "linksocks_ffi.dll",
    ]

    if any(p.exists() for p in ffi_candidates):
        return

    print("linksocks_ffi not built, building C-ABI shared library...")

    # Determine availability of Go sources
    local_go_src_dir = here / "linksocks_go"
    local_go_mod = here / "go.mod"
    have_local_sources = local_go_src_dir.exists() and (local_go_src_dir / "_python.go").exists() and local_go_mod.exists()
    if not have_local_sources:
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
        prepare_ffi_go_sources()
        env = os.environ.copy()
        env["CGO_ENABLED"] = "1"
        out_name = (
            "liblinksocks_ffi.so"
            if platform.system().lower() == "linux"
            else ("liblinksocks_ffi.dylib" if platform.system().lower() == "darwin" else "linksocks_ffi.dll")
        )
        run_command(
            [
                "go",
                "build",
                "-buildmode=c-shared",
                "-o",
                str(ffi_dir / out_name),
                "./linksocks_go_ffi",
            ],
            cwd=here,
            env=env,
        )
        print("Built linksocks_ffi shared library")
        return
    except Exception as e:
        raise RuntimeError(
            f"Failed to build linksocks_ffi shared library: {e}\n"
            "Use a pre-built wheel, or ensure Go 1.21+ is installed."
        )

def test_bindings():
    """Test if the Python bindings work correctly."""
    try:
        # Try to import the bindings
        sys.path.insert(0, str(here))
        import linksocks_ffi
        print("✓ Python bindings imported successfully")

        if hasattr(linksocks_ffi, "Client") and hasattr(linksocks_ffi, "Server"):
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
    "cffi>=1.15",
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


try:
    from wheel.bdist_wheel import bdist_wheel as _bdist_wheel
except Exception:
    _bdist_wheel = None


class BdistWheelFFI(_bdist_wheel if _bdist_wheel is not None else object):
    def finalize_options(self):
        if _bdist_wheel is None:
            return
        super().finalize_options()
        # This project ships a platform-specific shared library via package_data,
        # but it is not a CPython extension module.
        self.root_is_pure = False

    def get_tag(self):
        if _bdist_wheel is None:
            return ("py3", "none", "any")
        _python, _abi, plat = super().get_tag()
        return ("py3", "none", plat)

class BinaryDistribution(setuptools.Distribution):
    def has_ext_modules(_):
        return True

def configure_python_env(target_python: str, env: dict) -> dict:
    """Configure CGO flags for the current platform using the target Python interpreter.

    On Windows: use the target interpreter layout for include/libs.
    """
    system_name = platform.system().lower()
    try:
        if system_name == "windows":
            # Use target_python to determine paths, not sys.executable
            target_py = Path(target_python)
            py_dir = target_py.parent
            
            # Get version from target Python, not current interpreter
            try:
                version_output = subprocess.run(
                    [str(target_py), "-c", "import sys; print(f'{sys.version_info.major}{sys.version_info.minor}')"],
                    capture_output=True, text=True, check=True
                ).stdout.strip()
                py_version = version_output
            except Exception:
                # Fallback to current interpreter version
                py_version = f"{sys.version_info.major}{sys.version_info.minor}"
            
            # On Windows with venv, Python.exe is in Scripts/, but include/libs are in the base Python install
            # Try to find the real Python installation
            include_dir = py_dir / "include"
            libs_dir = py_dir / "libs"
            
            # If not found, check parent (for venv where python is in Scripts/)
            if not include_dir.exists() or not libs_dir.exists():
                # Try the venv's base Python (pyvenv.cfg points to it)
                pyvenv_cfg = py_dir.parent / "pyvenv.cfg"
                if pyvenv_cfg.exists():
                    try:
                        cfg_text = pyvenv_cfg.read_text()
                        for line in cfg_text.splitlines():
                            if line.startswith("home"):
                                base_path = Path(line.split("=", 1)[1].strip())
                                if (base_path / "include").exists():
                                    include_dir = base_path / "include"
                                if (base_path / "libs").exists():
                                    libs_dir = base_path / "libs"
                                break
                    except Exception:
                        pass
            
            print(f"Windows CGO config: py_version={py_version}, include={include_dir}, libs={libs_dir}")

            env["CGO_ENABLED"] = "1"
            # Use C17 standard for broad compiler compatibility.
            env["CC"] = "gcc -std=gnu17"
            cflags = []
            if include_dir.exists():
                cflags.append(f"-I{include_dir}")
            if env.get("CGO_CFLAGS"):
                cflags.append(env["CGO_CFLAGS"])
            env["CGO_CFLAGS"] = " ".join(cflags).strip()

            ldflags = []
            if libs_dir.exists():
                ldflags.append(f"-L{libs_dir}")
            ldflags.append(f"-lpython{py_version}")
            if env.get("CGO_LDFLAGS"):
                ldflags.append(env["CGO_LDFLAGS"])
            env["CGO_LDFLAGS"] = " ".join(ldflags).strip()
            return env
        elif system_name == "darwin":
            # macOS 15+ clang treats -Ofast as deprecated error
            env["CC"] = "clang -Wno-deprecated"
            return env
        return env
    except Exception as cfg_err:
        print(f"Warning: failed to configure platform CGO flags: {cfg_err}")
        return env

setup(
    name="linksocks",
    version="1.7.14",
    description="Python bindings for LinkSocks - SOCKS proxy over WebSocket",
    long_description=get_long_description(),
    long_description_content_type="text/markdown",
    author="jackzzs",
    author_email="jackzzs@outlook.com",
    url="https://github.com/linksocks/linksocks",
    license="MIT",
    
    # Package configuration
    packages=find_packages(include=["linksocks", "linksocks.*", "linksocks_ffi", "linksocks_ffi.*"]),
    package_data={
        "linksocks_ffi": ["*.py", "*.so", "*.dll", "*.dylib", "*.h"],
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
        "Bug Reports": "https://github.com/linksocks/linksocks/issues",
        "Source": "https://github.com/linksocks/linksocks",
        "Documentation": "https://github.com/linksocks/linksocks#readme",
        "Changelog": "https://github.com/linksocks/linksocks/releases",
    },
    
    # Binary distribution
    distclass=BinaryDistribution,
    cmdclass={
        "sdist": SdistWithGoSources,
        "build_py": BuildPyEnsureBindings,
        "develop": DevelopEnsureBindings,
        "install": InstallEnsureBindings,
        **({"bdist_wheel": BdistWheelFFI} if _bdist_wheel is not None else {}),
    },
)
