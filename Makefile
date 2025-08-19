# Makefile for linksocks project
.PHONY: help build clean test python-clean python-install python-test python-test-lib python-test-wrapper python-test-crash python-wheel

# Python binding targets
PYTHON_OUTPUT_DIR = _bindings/python
PYBIN ?= python3
PIP ?= $(PYBIN) -m pip

# Default target
help:
	@echo "Available targets:"
	@echo "  build           - Build Go binaries"
	@echo "  test            - Run Go tests"
	@echo "  clean           - Clean build artifacts"
	@echo "  python-clean    - Clean Python bindings"
	@echo "  python-install  - Install Python bindings to current python environment"
	@echo "  python-wheel    - Build Python wheel"
	@echo "  python-test     - Test Python bindings"

# Go targets
build:
	GOFLAGS="${GOFLAGS} -buildvcs=false" go build -o bin/linksocks cmd/linksocks/main.go

test:
	go test -v -race -coverprofile="coverage.txt" -covermode=atomic -coverpkg="./linksocks" "./linksocks" "./tests"

clean:
	rm -rf bin

python-clean:
	rm -rf $(PYTHON_OUTPUT_DIR)/linksockslib
	rm -rf $(PYTHON_OUTPUT_DIR)/linksocks.egg-info
	rm -rf $(PYTHON_OUTPUT_DIR)/build
	rm -rf $(PYTHON_OUTPUT_DIR)/dist

python-test-deps:
	@echo "Installing Python dev dependencies..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pip install -e .[dev]

python-test-lib:
	@echo "Running Python binding library tests..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_lib.py -v --tb=short -n auto

python-test-wrapper:
	@echo "Running Python wrapper tests..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_wrapper.py -v --tb=short -n auto

python-test-crash:
	@echo "Running Python wrapper crash tests..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests/test_crash.py -v --tb=short -n auto

python-test:
	@echo "Running Python wrapper crash tests..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pytest tests -v --tb=short -n auto

python-install:
	@echo "Installing Python bindings via pip (setup.py will drive the build)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pip install -e .

python-wheel:
	@echo "Building Python wheel via PEP 517 (setup.py logic is reused)..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m build --wheel