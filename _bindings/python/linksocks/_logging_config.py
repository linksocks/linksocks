"""
Logging configuration module for linksocks CLI.

This module provides custom logging handlers and configuration utilities
for the linksocks command-line interface, including Rich-based formatting
and loguru integration.
"""

import inspect
import logging
import asyncio
from typing import Any

from loguru import logger
from rich.text import Text
from rich.console import Console
from rich.logging import RichHandler

# Global console instance for consistent output
console = Console(stderr=True)


class CarriageReturnRichHandler(RichHandler):
    """
    A custom RichHandler that outputs a carriage return (\\r) before emitting log messages.
    
    This handler prevents Ctrl+C interruptions from disrupting the output by ensuring 
    the cursor is positioned at the beginning of the line before writing log messages.
    This is particularly useful in CLI applications where clean output formatting
    is important even during interruptions.
    
    Attributes:
        console: The Rich Console instance used for output formatting
    """
    
    def emit(self, record: logging.LogRecord) -> None:
        """
        Emit a log record with carriage return prefix.
        
        This method overrides the parent emit method to first output a carriage 
        return character, moving the cursor to the beginning of the line before 
        writing the actual log message. This prevents Ctrl+C characters from 
        appearing in the middle of log output.
        
        Args:
            record: The LogRecord instance containing the log message and metadata
            
        Note:
            The carriage return is only written if the console is a terminal.
            Errors during emission are handled by the parent class's handleError method.
        """
        try:
            # Write carriage return to move cursor to line start
            if self.console.is_terminal:
                self.console.file.write('\r')
                self.console.file.flush()
            
            # Call the parent emit method to handle the actual log output
            super().emit(record)
            
        except Exception:
            # Let the parent class handle any errors during log emission
            self.handleError(record)


class InterceptHandler(logging.Handler):
    """
    A logging handler that intercepts standard Python logging messages and redirects them to loguru.
    
    This handler bridges the gap between Python's standard logging module and loguru,
    allowing all log messages to be processed through loguru's enhanced formatting
    and filtering capabilities while maintaining compatibility with existing code
    that uses standard logging.
    
    Attributes:
        level: The minimum log level this handler will process
    """

    def __init__(self, level: int = 0) -> None:
        """
        Initialize the InterceptHandler.
        
        Args:
            level: The minimum logging level to handle (default: 0 for all levels)
        """
        super().__init__(level)

    def emit(self, record: logging.LogRecord) -> None:
        """
        Intercept a logging record and redirect it to loguru.
        
        This method converts standard Python logging records to loguru format,
        preserving the original log level, message content, and exception information.
        It also attempts to maintain the correct call stack depth for accurate
        source location reporting.
        
        Args:
            record: The LogRecord instance from Python's standard logging
        """
        try:
            # Try to map the logging level name to loguru's level system
            level = logger.level(record.levelname).name
        except ValueError:
            # If the level name is not recognized, use the numeric level
            level = record.levelno
            
        # Find the correct frame in the call stack to report accurate source location
        frame = inspect.currentframe()
        depth = 0
        while frame and (depth == 0 or frame.f_code.co_filename == logging.__file__):
            frame = frame.f_back
            depth += 1
            
        # Extract and clean the log message text
        text = record.getMessage()
        # Convert ANSI escape sequences to plain text for consistent formatting
        text = Text.from_ansi(text).plain
        
        # Forward the message to loguru with preserved context
        logger.opt(depth=depth, exception=record.exc_info).log(level, text)


def apply_logging_adapter(level: int = logging.INFO) -> None:
    """
    Configure Python's standard logging to use the InterceptHandler.
    
    This function replaces the standard logging configuration with our custom
    InterceptHandler, ensuring all logging messages are processed through loguru.
    
    Args:
        level: The minimum logging level to capture (default: logging.INFO)
        
    Note:
        This function uses force=True to override any existing logging configuration.
    """
    logging.basicConfig(handlers=[InterceptHandler()], level=level, force=True)


def init_logging(level: int = logging.INFO, **kwargs: Any) -> None:
    """
    Initialize the complete logging configuration for the CLI application.
    
    This function sets up a comprehensive logging system that:
    - Intercepts standard Python logging and routes it through loguru
    - Configures Rich-based formatting for enhanced terminal output
    - Supports custom log levels including trace-level debugging
    - Provides consistent formatting with timestamps
    
    Args:
        level: The minimum logging level to display (default: logging.INFO)
        **kwargs: Additional keyword arguments passed to CarriageReturnRichHandler
        
    The logging system supports the following levels:
    - logging.INFO (20): Standard informational messages
    - logging.DEBUG (10): Detailed debugging information
    - 5: Trace-level debugging (most verbose)
    
    Example:
        >>> init_logging(level=logging.DEBUG)
        >>> logger.info("Application started")
        >>> logger.debug("Debug information")
    """
    # Set up the logging adapter to intercept standard logging
    apply_logging_adapter(level)
    
    # Remove any existing loguru handlers to start fresh
    logger.remove()
    
    # Create our custom Rich handler with carriage return support
    handler = CarriageReturnRichHandler(
        console=console, 
        markup=True, 
        rich_tracebacks=True, 
        tracebacks_suppress=[asyncio], 
        **kwargs
    )
    
    # Set a simple timestamp format for log messages
    handler.setFormatter(logging.Formatter(None, "[%m/%d %H:%M]"))
    
    # Add the handler to loguru with minimal formatting (Rich handles the styling)
    logger.add(handler, format="{message}", level=level)