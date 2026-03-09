#!/usr/bin/env python3

from __future__ import annotations

import os
import platform
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

from setuptools import setup, find_packages
from setuptools.command.build_py import build_py as _build_py

try:
    from setuptools.command.editable_wheel import editable_wheel as _editable_wheel  # type: ignore
except Exception:
    _editable_wheel = None


here = Path(__file__).parent.absolute()
project_root = here.parent.parent


def run(cmd: list[str], cwd: Path | None = None, env: dict[str, str] | None = None) -> None:
    if env is None:
        env = os.environ.copy()
    subprocess.run(cmd, cwd=str(cwd) if cwd else None, env=env, check=True)


def ensure_go_sources(dst: Path) -> None:
    if (dst / "_python.go").exists() and (here / "go.mod").exists():
        return
    if not (project_root / "go.mod").exists():
        raise RuntimeError("Cannot find project root go.mod")

    dst.mkdir(parents=True, exist_ok=True)
    src_dir = project_root / "linksocks"
    if not src_dir.exists():
        raise RuntimeError("Cannot find linksocks Go sources")
    for go_file in src_dir.glob("*.go"):
        shutil.copy2(go_file, dst / go_file.name)


def ensure_embedded_go_mod_files() -> None:
    for f in ["go.mod", "go.sum"]:
        dst = here / f
        if dst.exists():
            continue
        src = project_root / f
        if not src.exists():
            raise RuntimeError(f"Cannot find {f} for embedded build")
        shutil.copy2(src, dst)


def cleanup_embedded_go_mod_files() -> None:
    for f in ["go.mod", "go.sum"]:
        p = here / f
        try:
            p.unlink()
        except FileNotFoundError:
            pass


def select_go_package_dir() -> tuple[Path, Path]:
    src_pkg = project_root / "linksocks"
    if (project_root / "go.mod").exists() and (src_pkg / "_python.go").exists():
        # Ensure we don't shadow the repo module with an embedded go.mod under _bindings/python_gopy.
        cleanup_embedded_go_mod_files()
        return src_pkg, project_root

    embedded_pkg = here / "linksocks_go"
    ensure_embedded_go_mod_files()
    ensure_go_sources(embedded_pkg)
    return embedded_pkg, here


def build_gopy_bindings() -> None:
    out_dir = here / "linksockslib"
    go_pkg_dir, go_cwd = select_go_package_dir()

    env = os.environ.copy()
    env["GOFLAGS"] = (env.get("GOFLAGS", "") + " -buildvcs=false").strip()
    env["CGO_ENABLED"] = "1"
    gomod = go_cwd / "go.mod"
    if gomod.exists():
        env["GOMOD"] = str(gomod)
    # Some Python distributions (e.g. conda) inject toolchain flags like `-B <dir>`
    # into LDFLAGS, which Go rejects by default when parsing `#cgo LDFLAGS`.
    # Allow `-B` explicitly for local builds.
    allow = env.get("CGO_LDFLAGS_ALLOW", "").strip()
    patterns = [
        "^-B$",
        "^/.*$",
    ]
    for pat in patterns:
        if allow:
            allow = allow + "|" + pat
        else:
            allow = pat
    env["CGO_LDFLAGS_ALLOW"] = allow

    tmpdir = Path(tempfile.mkdtemp(prefix="linksockslib_build_"))
    try:
        venv_dir = tmpdir / "venv"
        run([sys.executable, "-m", "venv", str(venv_dir)])
        scripts_dir = venv_dir / ("Scripts" if platform.system().lower() == "windows" else "bin")
        py = scripts_dir / ("python.exe" if platform.system().lower() == "windows" else "python")
        run([str(py), "-m", "pip", "install", "--upgrade", "pip"])
        run([str(py), "-m", "pip", "install", "pybindgen", "setuptools", "wheel"])

        gopy_exe = scripts_dir / ("gopy.exe" if platform.system().lower() == "windows" else "gopy")
        gopy_version = os.environ.get("LINKSOCKSLIB_GOPY_VERSION", "latest")

        go_env = env.copy()
        go_env["GOBIN"] = str(scripts_dir)
        go_env["GOCACHE"] = str(tmpdir / "gocache")
        go_env["GOMODCACHE"] = str(tmpdir / "gomodcache")

        # Install gopy as a Go tool. The PyPI 'gopy' package is not executable via `python -m gopy`.
        run(["go", "install", f"github.com/go-python/gopy@{gopy_version}"], env=go_env)
        if not gopy_exe.exists():
            raise RuntimeError(f"gopy executable was not installed at {gopy_exe}")

        # gopy writes into out_dir
        if out_dir.exists():
            shutil.rmtree(out_dir)
        out_dir.mkdir(parents=True, exist_ok=True)

        pkg_rel = os.path.relpath(str(go_pkg_dir), str(go_cwd)).replace(os.sep, "/")
        pkg_arg = pkg_rel if pkg_rel.startswith(".") else f"./{pkg_rel}"

        cmd = [
            str(gopy_exe),
            "build",
            f"-vm={py}",
            f"-output={out_dir}",
            "-name=linksockslib",
            "-no-make=true",
            "-dynamic-link=true",
            pkg_arg,
        ]
        run(cmd, cwd=go_cwd, env=env)

        patch_generated_bindings(out_dir)

        init_py = out_dir / "__init__.py"
        if not init_py.exists():
            init_py.write_text("", encoding="utf-8")
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)


def patch_generated_bindings(out_dir: Path) -> None:
    go_file = out_dir / "linksockslib.go"
    if not go_file.exists():
        return

    source = go_file.read_text(encoding="utf-8")
    helper = '''func setPyRuntimeError(msg string) *C.char {
	estr := C.CString(msg)
	C.PyErr_SetString(C.PyExc_RuntimeError, estr)
	return estr
}

// --- generated code for package: linksockslib below: ---
'''
    marker = '// --- generated code for package: linksockslib below: ---\n'
    if "func setPyRuntimeError(msg string) *C.char" not in source and marker in source:
        source = source.replace(marker, helper, 1)

    changed = False
    replacements = [
        (
            r'//export linksocks_LinkSocksClient_WaitReady\nfunc linksocks_LinkSocksClient_WaitReady\(_handle CGoHandle, ctx CGoHandle, timeout C\.longlong\) \*C\.char \{\n(?:.|\n)*?\n\}\n\n//export linksocks_LinkSocksClient_Connect',
            '''//export linksocks_LinkSocksClient_WaitReady
func linksocks_LinkSocksClient_WaitReady(_handle CGoHandle, ctx CGoHandle, timeout C.longlong) (ret *C.char) {
	_saved_thread := C.PyEval_SaveThread()
	defer func() {
		C.PyEval_RestoreThread(_saved_thread)
		if r := recover(); r != nil {
			ret = setPyRuntimeError(fmt.Sprintf("panic in LinkSocksClient.WaitReady: %v", r))
		}
	}()
	vifc, __err := gopyh.VarFromHandleTry((gopyh.CGoHandle)(_handle), "*linksocks.LinkSocksClient")
	if __err != nil {
		return setPyRuntimeError(__err.Error())
	}
	__err = gopyh.Embed(vifc, reflect.TypeOf(linksocks.LinkSocksClient{})).(*linksocks.LinkSocksClient).WaitReady(ptrFromHandle_context_Context(ctx), time.Duration(int64(timeout)))
	if __err != nil {
		return setPyRuntimeError(__err.Error())
	}
	return C.CString("")
}

//export linksocks_LinkSocksClient_Connect''',
        ),
        (
            r'//export linksocks_LinkSocksClient_Close\nfunc linksocks_LinkSocksClient_Close\(_handle CGoHandle, goRun C\.char\) \{\n(?:.|\n)*?\n\}\n\n//export linksocks_LinkSocksClient_AddConnector',
            '''//export linksocks_LinkSocksClient_Close
func linksocks_LinkSocksClient_Close(_handle CGoHandle, goRun C.char) {
	_saved_thread := C.PyEval_SaveThread()
	defer func() {
		C.PyEval_RestoreThread(_saved_thread)
		if r := recover(); r != nil {
			setPyRuntimeError(fmt.Sprintf("panic in LinkSocksClient.Close: %v", r))
		}
	}()
	vifc, __err := gopyh.VarFromHandleTry((gopyh.CGoHandle)(_handle), "*linksocks.LinkSocksClient")
	if __err != nil {
		setPyRuntimeError(__err.Error())
		return
	}
	if boolPyToGo(goRun) {
		go gopyh.Embed(vifc, reflect.TypeOf(linksocks.LinkSocksClient{})).(*linksocks.LinkSocksClient).Close()
	} else {
		gopyh.Embed(vifc, reflect.TypeOf(linksocks.LinkSocksClient{})).(*linksocks.LinkSocksClient).Close()
	}
}

//export linksocks_LinkSocksClient_AddConnector''',
        ),
        (
            r'//export linksocks_LinkSocksServer_WaitReady\nfunc linksocks_LinkSocksServer_WaitReady\(_handle CGoHandle, ctx CGoHandle, timeout C\.longlong\) \*C\.char \{\n(?:.|\n)*?\n\}\n\n//export linksocks_LinkSocksServer_Close',
            '''//export linksocks_LinkSocksServer_WaitReady
func linksocks_LinkSocksServer_WaitReady(_handle CGoHandle, ctx CGoHandle, timeout C.longlong) (ret *C.char) {
	_saved_thread := C.PyEval_SaveThread()
	defer func() {
		C.PyEval_RestoreThread(_saved_thread)
		if r := recover(); r != nil {
			ret = setPyRuntimeError(fmt.Sprintf("panic in LinkSocksServer.WaitReady: %v", r))
		}
	}()
	vifc, __err := gopyh.VarFromHandleTry((gopyh.CGoHandle)(_handle), "*linksocks.LinkSocksServer")
	if __err != nil {
		return setPyRuntimeError(__err.Error())
	}
	__err = gopyh.Embed(vifc, reflect.TypeOf(linksocks.LinkSocksServer{})).(*linksocks.LinkSocksServer).WaitReady(ptrFromHandle_context_Context(ctx), time.Duration(int64(timeout)))
	if __err != nil {
		return setPyRuntimeError(__err.Error())
	}
	return C.CString("")
}

//export linksocks_LinkSocksServer_Close''',
        ),
        (
            r'//export linksocks_LinkSocksServer_Close\nfunc linksocks_LinkSocksServer_Close\(_handle CGoHandle, goRun C\.char\) \{\n(?:.|\n)*?\n\}\n\n//export linksocks_LinkSocksServer_GetClientCount',
            '''//export linksocks_LinkSocksServer_Close
func linksocks_LinkSocksServer_Close(_handle CGoHandle, goRun C.char) {
	_saved_thread := C.PyEval_SaveThread()
	defer func() {
		C.PyEval_RestoreThread(_saved_thread)
		if r := recover(); r != nil {
			setPyRuntimeError(fmt.Sprintf("panic in LinkSocksServer.Close: %v", r))
		}
	}()
	vifc, __err := gopyh.VarFromHandleTry((gopyh.CGoHandle)(_handle), "*linksocks.LinkSocksServer")
	if __err != nil {
		setPyRuntimeError(__err.Error())
		return
	}
	if boolPyToGo(goRun) {
		go gopyh.Embed(vifc, reflect.TypeOf(linksocks.LinkSocksServer{})).(*linksocks.LinkSocksServer).Close()
	} else {
		gopyh.Embed(vifc, reflect.TypeOf(linksocks.LinkSocksServer{})).(*linksocks.LinkSocksServer).Close()
	}
}

//export linksocks_LinkSocksServer_GetClientCount''',
        ),
    ]
    for pattern, replacement in replacements:
        new_source, count = re.subn(pattern, replacement, source, count=1, flags=re.S)
        if count:
            source = new_source
            changed = True

    if changed:
        go_file.write_text(source, encoding="utf-8")


class BuildGopyOnBuildPy(_build_py):
    def run(self):
        build_gopy_bindings()
        super().run()

if _editable_wheel is not None:
    class BuildGopyOnEditableWheel(_editable_wheel):
        def run(self):
            build_gopy_bindings()
            super().run()

setup(
    name="linksockslib",
    version="1.8.1",
    description="gopy backend package for linksocks",
    long_description="gopy backend package containing the linksockslib extension.",
    long_description_content_type="text/plain",
    author="jackzzs",
    url="https://github.com/linksocks/linksocks",
    license="MIT",
    # Ensure package discovery works under PEP 517/660 (editable installs).
    # Otherwise, find_packages() may see no packages and pip installs nothing.
    packages=find_packages(include=["linksockslib", "linksockslib.*", "linksocks", "linksocks.*"]),
    package_data={
        "linksockslib": ["*.py", "*.so", "*.pyd", "*.dll", "*.dylib", "*.h", "*.c", "*.go"],
    },
    include_package_data=True,
    python_requires=">=3.9",
    extras_require={
        "dev": [
            "pytest>=6.0",
            "pytest-cov>=2.10",
            "pytest-mock>=3.0",
            "pytest-xdist",
            "httpx[socks]",
            "requests",
            "pysocks",
        ],
    },
    zip_safe=False,
    cmdclass={
        "build_py": BuildGopyOnBuildPy,
        **({"editable_wheel": BuildGopyOnEditableWheel} if _editable_wheel is not None else {}),
    },
)
