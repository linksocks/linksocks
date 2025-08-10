# Makefile for wssocks project
.PHONY: help build clean test python-bindings python-clean python-install python-test python-test-lib python-test-wrapper python-test-crash python-bindings-deps

# Python binding targets
PYTHON_OUTPUT_DIR = _bindings/python

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
	go build -o bin/wssocks cmd/wssocks/main.go

test:
	go test -v -race -coverprofile="coverage.txt" -covermode=atomic -coverpkg="./wssocks" "./wssocks" "./tests"

clean:
	rm -rf bin

python-bindings-deps:
	@echo "Installing Go dependencies..."
	go install github.com/go-python/gopy@latest
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Installing Python dependencies..."
	pip3 install pybindgen wheel setuptools pytest

python-bindings: python-clean
	@echo "Generating Python bindings with gopy pkg..."
	@orig="wssocks/_python.go"; tmp="wssocks/python.go"; rc=0; \
	if [ -f "$$orig" ]; then cp "$$orig" "$$tmp"; fi; \
	gopy build -vm=python3 -output=$(PYTHON_OUTPUT_DIR)/wssockslib -name=wssockslib -no-make=true \
		-build-tags=gopy github.com/zetxtech/wssocks/wssocks || rc=$$?; \
	echo "from .wssocks import *" > $(PYTHON_OUTPUT_DIR)/wssockslib/__init__.py; \
	rm -f "$$tmp"; \
	exit $$rc
		
python-clean:
	rm -rf $(PYTHON_OUTPUT_DIR)/wssockslib
	rm -rf $(PYTHON_OUTPUT_DIR)/wssocks.egg-info

python-test-deps:
	@echo "Installing Python dev dependencies..."
	cd $(PYTHON_OUTPUT_DIR) && python3 -m pip install -e .[dev]

python-test-lib:
	@echo "Running Python binding library tests..."
	cd $(PYTHON_OUTPUT_DIR) && python3 -m pytest tests/test_lib.py -v --tb=short -n auto

python-test-wrapper:
	@echo "Running Python wrapper tests..."
	cd $(PYTHON_OUTPUT_DIR) && python3 -m pytest tests/test_wrapper.py -v --tb=short -n auto

python-test-crash:
	@echo "Running Python wrapper crash tests..."
	cd $(PYTHON_OUTPUT_DIR) && python3 -m pytest tests/test_crash.py -v --tb=short -n auto

python-test:
	@echo "Running Python wrapper crash tests..."
	cd $(PYTHON_OUTPUT_DIR) && python3 -m pytest tests -v --tb=short -n auto

python-install: python-bindings
	@echo "Installing Python bindings..."
	cd $(PYTHON_OUTPUT_DIR) && python3 -m pip install -e .