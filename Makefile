# Makefile for linksocks project
.PHONY: help build clean test python-bindings python-clean python-install python-test python-test-lib python-test-wrapper python-test-crash python-bindings-deps

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
	@echo "  python-bindings - Generate Python bindings using gopy"
	@echo "  python-clean    - Clean Python bindings"
	@echo "  python-install  - Install Python bindings locally"
	@echo "  python-wheel    - Build Python wheel"
	@echo "  python-test     - Test Python bindings"

# Go targets
build:
	go build -o bin/linksocks cmd/linksocks/main.go

test:
	go test -v -race -coverprofile="coverage.txt" -covermode=atomic -coverpkg="./linksocks" "./linksocks" "./tests"

clean:
	rm -rf bin

python-bindings-deps:
	@echo "Installing Go dependencies..."
	go install github.com/go-python/gopy@v0.4.7
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Installing Python dependencies..."
	$(PIP) install pybindgen wheel setuptools pytest

python-bindings: python-clean
	@echo "Generating Python bindings with gopy pkg..."
	@orig="linksocks/_python.go"; tmp="linksocks/python.go"; rc=0; \
	if [ -f "$$orig" ]; then cp "$$orig" "$$tmp"; fi; \
	gopy build -vm=$(PYBIN) -output=$(PYTHON_OUTPUT_DIR)/linksockslib -name=linksockslib -no-make=true \
		-build-tags=gopy github.com/zetxtech/linksocks/linksocks || rc=$$?; \
	echo "from .linksocks import *" > $(PYTHON_OUTPUT_DIR)/linksockslib/__init__.py; \
	rm -f "$$tmp"; \
	go mod tidy; \
	exit $$rc
		
python-clean:
	rm -rf $(PYTHON_OUTPUT_DIR)/linksockslib
	rm -rf $(PYTHON_OUTPUT_DIR)/linksocks.egg-info

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

python-install: python-bindings
	@echo "Installing Python bindings..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m pip install -e .

python-wheel: python-bindings
	@echo "Building Python wheel..."
	cd $(PYTHON_OUTPUT_DIR) && $(PYBIN) -m build --wheel --outdir ../../dist