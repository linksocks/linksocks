# Makefile for linksocks project
.PHONY: help build clean test \
	python-cffi-build python-cffi-clean \
	python-clean python-install python-wheel python-test python-test-deps python-test-wrapper python-test-crash \
	python-gopy-clean python-gopy-install python-gopy-wheel python-gopy-test python-gopy-test-deps python-gopy-test-lib python-gopy-test-wrapper python-gopy-test-crash

# Python binding targets
PYTHON_OUTPUT_DIR = _bindings/python
PYBIN ?= python3
PIP ?= $(PYBIN) -m pip

# CFFI (Go c-shared) output
PYTHON_CFFI_DIR = $(PYTHON_OUTPUT_DIR)/linksocks_ffi
PYTHON_CFFI_SHIM = $(PYTHON_OUTPUT_DIR)/linksocks_go_ffi
PYTHON_CFFI_GO_PKG = ./$(PYTHON_CFFI_SHIM)

GOOS ?= $(shell go env GOOS)
ifeq ($(GOOS),darwin)
PYTHON_CFFI_LIB = $(PYTHON_CFFI_DIR)/liblinksocks_ffi.dylib
else ifeq ($(GOOS),windows)
PYTHON_CFFI_LIB = $(PYTHON_CFFI_DIR)/linksocks_ffi.dll
else
PYTHON_CFFI_LIB = $(PYTHON_CFFI_DIR)/liblinksocks_ffi.so
endif

# Default target
help:
	@echo "Available targets:"
	@echo "  build           - Build Go binaries"
	@echo "  test            - Run Go tests"
	@echo "  clean           - Clean build artifacts"
	@echo "  python-cffi-build   - Build Go C-ABI shared library for cffi"
	@echo "  python-cffi-clean   - Clean Go C-ABI shared library artifacts"
	@echo "  python-install      - Install Python package (default: cffi backend)"
	@echo "  python-wheel        - Build Python wheel (default: cffi backend)"
	@echo "  python-test         - Run Python tests (default: cffi backend; ignores gopy-only tests)"
	@echo "  python-gopy-install - Install Python package with gopy backend (LINKSOCKS_BUILD_GOPY=1)"
	@echo "  python-gopy-test    - Run Python tests with gopy backend"

# Go targets
build:
	GOFLAGS="${GOFLAGS} -buildvcs=false" go build -o bin/linksocks cmd/linksocks/main.go

test:
	go test -v -race -coverprofile="coverage.txt" -covermode=atomic -coverpkg="./linksocks" "./linksocks" "./tests"

clean:
	rm -rf bin

python-cffi-build:
	@mkdir -p $(PYTHON_CFFI_DIR)
	CGO_ENABLED=1 GOFLAGS="${GOFLAGS} -buildvcs=false" go build -buildmode=c-shared -o $(PYTHON_CFFI_LIB) $(PYTHON_CFFI_GO_PKG)
	@rm -f $(PYTHON_OUTPUT_DIR)/linksockslib/_linksockslib*.so \
		$(PYTHON_OUTPUT_DIR)/linksockslib/_linksockslib*.dylib \
		$(PYTHON_OUTPUT_DIR)/linksockslib/_linksockslib*.dll \
		$(PYTHON_OUTPUT_DIR)/linksockslib/_linksockslib*.pyd \
		$(PYTHON_OUTPUT_DIR)/linksockslib/_linksockslib*.h 2>/dev/null || true

python-cffi-clean:
	rm -f $(PYTHON_CFFI_DIR)/liblinksocks_ffi.so $(PYTHON_CFFI_DIR)/liblinksocks_ffi.dylib $(PYTHON_CFFI_DIR)/linksocks_ffi.dll
	rm -f $(PYTHON_CFFI_DIR)/liblinksocks_ffi.h

python-clean: python-cffi-clean
	rm -rf $(PYTHON_OUTPUT_DIR)/linksocks.egg-info
	rm -rf $(PYTHON_OUTPUT_DIR)/build
	rm -rf $(PYTHON_OUTPUT_DIR)/dist

python-test-deps:
	@echo "Installing Python dev dependencies..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pip install -e .[dev]

python-test-wrapper: python-cffi-build
	@echo "Running Python wrapper tests (cffi backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_wrapper.py -v --tb=short -n auto

python-test-crash: python-cffi-build
	@echo "Running Python wrapper crash tests (cffi backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_crash.py -v --tb=short -n auto

python-test: python-cffi-build
	@echo "Running Python tests (cffi backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests -v --tb=short -n auto --ignore=tests/test_lib.py

python-install:
	@echo "Installing Python package via pip (default: cffi backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pip install -e .

python-wheel:
	@echo "Building Python wheel (default: cffi backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m build --wheel

python-gopy-clean:
	rm -rf $(PYTHON_OUTPUT_DIR)/linksockslib
	rm -rf $(PYTHON_OUTPUT_DIR)/linksocks.egg-info
	rm -rf $(PYTHON_OUTPUT_DIR)/build
	rm -rf $(PYTHON_OUTPUT_DIR)/dist

python-gopy-test-deps:
	@echo "Installing Python dev dependencies (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && LINKSOCKS_BUILD_GOPY=1 $(PYBIN) -m pip install -e .[dev]

python-gopy-test-lib:
	@echo "Running Python binding library tests (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_lib.py -v --tb=short -n auto

python-gopy-test-wrapper:
	@echo "Running Python wrapper tests (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_wrapper.py -v --tb=short -n auto

python-gopy-test-crash:
	@echo "Running Python wrapper crash tests (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_crash.py -v --tb=short -n auto

python-gopy-test:
	@echo "Running Python tests (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests -v --tb=short -n auto

python-gopy-install:
	@echo "Installing Python package via pip (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && LINKSOCKS_BUILD_GOPY=1 $(PYBIN) -m pip install -e .

python-gopy-wheel:
	@echo "Building Python wheel (gopy backend)..."
	cd $(PYTHON_OUTPUT_DIR) && LINKSOCKS_BUILD_GOPY=1 $(PYBIN) -m build --wheel