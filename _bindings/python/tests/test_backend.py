import importlib.util
import pathlib


def _module_present(name: str) -> bool:
    return importlib.util.find_spec(name) is not None


def _ffi_artifact_present() -> bool:
    try:
        import linksocks_ffi

        ffi_dir = pathlib.Path(linksocks_ffi.__file__).resolve().parent
        return any(p.suffix in {".so", ".dylib", ".dll"} and "linksocks_ffi" in p.name for p in ffi_dir.iterdir())
    except Exception:
        return False


def _gopy_artifact_present() -> bool:
    # A built gopy backend produces an extension module under linksockslib.
    if not _module_present("linksockslib"):
        return False

    pkg_dir = pathlib.Path(__file__).resolve().parent.parent / "linksockslib"
    if not pkg_dir.exists():
        return False

    for p in pkg_dir.iterdir():
        if p.is_file() and p.name.startswith("_linksockslib") and p.suffix in {".so", ".pyd", ".dll", ".dylib"}:
            return True

    try:
        from linksockslib import linksocks  # noqa: F401

        return True
    except Exception:
        return False


def test_backend_exclusivity():
    ffi_present = _module_present("linksocks_ffi") and _ffi_artifact_present()
    gopy_present = _gopy_artifact_present()

    assert (ffi_present ^ gopy_present), (
        f"Expected exactly one backend present, got ffi={ffi_present}, gopy={gopy_present}. "
        "Build with default (ffi) or set LINKSOCKS_BUILD_GOPY=1 (gopy), but not both."
    )
