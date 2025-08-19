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
import importlib.machinery
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

def ensure_placeholder_linksockslib():
    """Ensure a placeholder Python package exists so find_packages() includes it.
    The actual native bindings will be generated later during the build step.
    """
    pkg_dir = here / "linksockslib"
    init_py = pkg_dir / "__init__.py"
    try:
        if not pkg_dir.exists():
            pkg_dir.mkdir(parents=True, exist_ok=True)
        if not init_py.exists():
            init_py.write_text("# Placeholder; real contents generated during build\n")
    except Exception as e:
        print(f"Warning: failed to create placeholder linksockslib: {e}")

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

def _expected_binary_names() -> list[str]:
    """Return candidate filenames for the extension for the current interpreter/platform."""
    candidates: list[str] = []
    for suffix in importlib.machinery.EXTENSION_SUFFIXES:
        # e.g. ['.cpython-311-x86_64-linux-gnu.so', '.so']
        candidates.append(f"_linksockslib{suffix}")
    # Also include conservative fallbacks by version tag just in case
    pyver = f"{sys.version_info.major}{sys.version_info.minor}"
    candidates.append(f"_linksockslib.cpython-{pyver}.so")
    candidates.append(f"_linksockslib.cp{pyver}.pyd")
    return candidates


def is_linksockslib_built(lib_dir: Path) -> bool:
    """Determine if linksockslib contains a native artifact compatible with this Python."""
    if not lib_dir.exists():
        return False
    for name in _expected_binary_names():
        if (lib_dir / name).exists():
            return True
    return False


def prune_foreign_binaries(lib_dir: Path) -> None:
    """Remove artifacts that are not compatible with the current interpreter.

    This prevents wheels for one Python version from accidentally bundling
    binaries produced for a different version/ABI.
    """
    if not lib_dir.exists():
        return
    keep_names = set(_expected_binary_names())
    for p in lib_dir.iterdir():
        if not p.is_file():
            continue
        if p.name.startswith("_linksockslib") and p.suffix in {".so", ".pyd", ".dll", ".dylib"}:
            if p.name not in keep_names:
                try:
                    p.unlink()
                    print(f"Pruned foreign binary: {p}")
                except Exception as e:
                    print(f"Warning: failed to remove {p}: {e}")

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

def install_gopy_and_tools():
    """Install gopy and related Go tools."""
    print("Installing gopy and Go tools...")
    
    # Ensure Go is available
    if not check_go_installation():
        raise RuntimeError("Go is not available after installation attempt")
    
    # Helper to add Go bin dirs (GOBIN/GOPATH/bin) to PATH so "gopy" is discoverable
    def _ensure_go_bins_on_path():
        try:
            gobin = run_command(["go", "env", "GOBIN"]) or ""
        except Exception:
            gobin = ""
        try:
            gopath = run_command(["go", "env", "GOPATH"]) or ""
        except Exception:
            gopath = ""

        candidate_dirs = []
        if gobin.strip():
            candidate_dirs.append(Path(gobin.strip()))
        if gopath.strip():
            candidate_dirs.append(Path(gopath.strip()) / "bin")

        current_path = os.environ.get("PATH", "")
        updated_parts = [p for p in current_path.split(os.pathsep) if p]
        for d in candidate_dirs:
            d_str = str(d)
            if d_str and d_str not in updated_parts:
                updated_parts.insert(0, d_str)
        os.environ["PATH"] = os.pathsep.join(updated_parts)

    # Install gopy
    try:
        run_command(["go", "install", "github.com/go-python/gopy@latest"])
        print("gopy installed successfully")
    except subprocess.CalledProcessError as e:
        print(f"Failed to install gopy: {e}")
        raise
    
    # Ensure the freshly installed Go binaries are on PATH (especially on Windows)
    _ensure_go_bins_on_path()

    # Install goimports
    try:
        run_command(["go", "install", "golang.org/x/tools/cmd/goimports@latest"])
        print("goimports installed successfully")
    except subprocess.CalledProcessError as e:
        print(f"Failed to install goimports: {e}")
        raise

def build_python_bindings(vm_python: Optional[str] = None):
    """Build Python bindings using gopy.

    vm_python: Optional path to the Python interpreter to target (e.g., from a venv).
    If not provided, defaults to the current interpreter.
    """
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
        # Allow any LDFLAGS through cgo validation
        env["CGO_LDFLAGS_ALLOW"] = ".*"
        # Determine target Python now so we can configure CGO flags appropriately
        target_vm = vm_python or sys.executable
        # Configure platform-specific CGO flags based on the target interpreter
        env = configure_python_env(target_vm, env)
        
        # Ensure PATH includes Go binary directory (for temp install path)
        if _temp_go_dir:
            go_bin = Path(_temp_go_dir) / "go" / "bin"
            current_path = env.get("PATH", "")
            if str(go_bin) not in current_path:
                env["PATH"] = f"{go_bin}{os.pathsep}{current_path}"
                print(f"Updated PATH for gopy execution: {go_bin}")

        # Also ensure GOBIN/GOPATH/bin are present for system Go installs
        try:
            gobin = run_command(["go", "env", "GOBIN"]) or ""
        except Exception:
            gobin = ""
        try:
            gopath = run_command(["go", "env", "GOPATH"]) or ""
        except Exception:
            gopath = ""
        candidate_bins = []
        if gobin.strip():
            candidate_bins.append(Path(gobin.strip()))
        if gopath.strip():
            candidate_bins.append(Path(gopath.strip()) / "bin")
        current_path = env.get("PATH", "")
        for b in candidate_bins:
            b_str = str(b)
            if b_str and b_str not in current_path:
                current_path = f"{b_str}{os.pathsep}{current_path}"
        env["PATH"] = current_path
        
        # Prefer absolute path to gopy if available
        gopy_executable = shutil.which("gopy", path=env.get("PATH", ""))
        if not gopy_executable:
            # Try common locations
            for b in candidate_bins:
                candidate = b / ("gopy.exe" if platform.system().lower() == "windows" else "gopy")
                if candidate.exists():
                    gopy_executable = str(candidate)
                    break
        if not gopy_executable:
            raise FileNotFoundError("gopy executable not found on PATH. Ensure GOBIN/GOPATH/bin is on PATH.")

        # Log environment before build for diagnostics
        print(f"Using gopy: {gopy_executable}")
        print(f"Using PATH: {env.get('PATH','')}")
        print(f"Using CGO_CFLAGS: {env.get('CGO_CFLAGS','')}")
        print(f"Using CGO_LDFLAGS: {env.get('CGO_LDFLAGS','')}")
        print(f"Using CGO_LDFLAGS_ALLOW: {env.get('CGO_LDFLAGS_ALLOW','')}")

        # Run gopy build from _bindings/python directory
        cmd = [
            gopy_executable, "build",
            f"-vm={target_vm}",
            f"-output={linksocks_lib_dir}",
            "-name=linksockslib",
            "-no-make=true",
        ]
        
        # Only enable dynamic-link on Linux
        if platform.system().lower() == "linux":
            cmd.append("-dynamic-link=true")
        
        cmd.append("./linksocks_go")  # Use linksocks_go directory
        
        run_command(cmd, cwd=here, env=env)
        
        # Clean up go.mod
        run_command(["go", "mod", "tidy"], cwd=here)
        
        print("Python bindings built successfully")
        # After a successful build, prune any binaries not matching current ABI
        prune_foreign_binaries(linksocks_lib_dir)
        
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
    
    # Decide based on whether a binding for THIS interpreter exists
    if not is_linksockslib_built(linksocks_lib_dir):
        print("linksockslib not built or only placeholder found, building Python bindings...")
        
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
            
            # Create an isolated temporary virtual environment for Python-side build deps
            venv_dir, venv_python = create_temp_virtualenv()
            pip_cmd = ensure_pip_for_python(venv_python)
            # Upgrade pip in the venv and install build deps strictly inside the venv
            run_command(pip_cmd + ["install", "--upgrade", "pip"])
            run_command(pip_cmd + ["install", "pybindgen", "wheel", "setuptools"])

            # Build bindings targeting the venv's Python
            build_python_bindings(vm_python=str(venv_python))
            
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
            # Clean up temporary Python virtual environment
            try:
                if _temp_py_venv_dir and Path(_temp_py_venv_dir).exists():
                    shutil.rmtree(_temp_py_venv_dir)
            except Exception as e:
                print(f"Warning: Failed to clean up temporary Python venv: {e}")
        
        if not is_linksockslib_built(linksocks_lib_dir):
            raise RuntimeError("Failed to build Python bindings (artifacts missing)")
    else:
        # Ensure we only ship binaries compatible with this interpreter
        prune_foreign_binaries(linksocks_lib_dir)
        print(f"Found existing built linksockslib at {linksocks_lib_dir}")

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
        # Ensure placeholder so that wheel metadata captures the package
        ensure_placeholder_linksockslib()
        try:
            ensure_python_bindings()
        except Exception as e:
            # Do not fail metadata-only operations; re-raise for real builds
            if os.environ.get("SETUPTOOLS_BUILD_META", ""):  # PEP 517 builds
                raise
            raise
        # Just in case, prune leftovers again before packaging
        prune_foreign_binaries(here / "linksockslib")
        super().run()


class DevelopEnsureBindings(_develop):
    """Ensure bindings exist for editable installs (pip install -e .)."""

    def run(self):
        ensure_placeholder_linksockslib()
        ensure_python_bindings()
        super().run()


class InstallEnsureBindings(_install):
    """Ensure bindings exist for regular installs (pip install .)."""

    def run(self):
        ensure_placeholder_linksockslib()
        ensure_python_bindings()
        super().run()

class BinaryDistribution(setuptools.Distribution):
    def has_ext_modules(_):
        return True

def configure_python_env(target_python: str, env: dict) -> dict:
    """Configure CGO flags for the current platform using the target Python interpreter.

    On Windows: use the current interpreter layout for include/libs.
    """
    system_name = platform.system().lower()
    try:
        if system_name == "windows":
            py_version = f"{sys.version_info.major}{sys.version_info.minor}"
            py_dir = Path(sys.executable).parent
            include_dir = py_dir / "include"
            libs_dir = py_dir / "libs"

            env["CGO_ENABLED"] = "1"
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
        return env
    except Exception as cfg_err:
        print(f"Warning: failed to configure platform CGO flags: {cfg_err}")
        return env

# Ensure placeholder package exists BEFORE calling setup() so find_packages() sees it
ensure_placeholder_linksockslib()

setup(
    name="linksocks",
    version="3.0.12",
    description="Python bindings for LinkSocks - SOCKS proxy over WebSocket",
    long_description=get_long_description(),
    long_description_content_type="text/markdown",
    author="jackzzs",
    author_email="jackzzs@outlook.com",
    url="https://github.com/linksocks/linksocks",
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
    },
)
