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
def test_backend_exclusivity():
    ffi_present = _module_present("linksocks_ffi") and _ffi_artifact_present()

    assert ffi_present, "Expected linksocks_ffi backend present for the default package"
