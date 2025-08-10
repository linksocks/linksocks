"""
Utility functions for linksocks CLI.

This module provides common utility functions used across the linksocks
command-line interface, including SOCKS proxy URL parsing and environment
variable handling.
"""

import os
import urllib.parse
from typing import Optional, Tuple


def parse_socks_proxy(proxy_url: str) -> Tuple[str, Optional[str], Optional[str]]:
    """
    Parse a SOCKS5 proxy URL and extract connection details.
    
    This function parses SOCKS5 proxy URLs in the standard format and extracts
    the host address, username, and password for authentication. It supports
    URLs with optional authentication credentials and port numbers.
    
    Args:
        proxy_url: URL string in format socks5://[user:pass@]host[:port]
                  Examples:
                  - "socks5://127.0.0.1:9870"
                  - "socks5://user:pass@proxy.example.com:9870"
                  - "socks5://proxy.example.com" (uses default port 9870)
        
    Returns:
        A tuple containing:
        - address (str): Host and port in "host:port" format
        - username (str | None): Username for authentication, if provided
        - password (str | None): Password for authentication, if provided
        
    Raises:
        ValueError: If the URL is invalid, malformed, or uses an unsupported scheme
        
    Examples:
        >>> parse_socks_proxy("socks5://127.0.0.1:9870")
        ("127.0.0.1:9870", None, None)
        
        >>> parse_socks_proxy("socks5://user:pass@proxy.example.com:9870")
        ("proxy.example.com:9870", "user", "pass")
        
        >>> parse_socks_proxy("socks5://proxy.example.com")
        ("proxy.example.com:9870", None, None)
    """
    if not proxy_url:
        return "", None, None

    try:
        parsed_url = urllib.parse.urlparse(proxy_url)
    except Exception as e:
        raise ValueError(f"Invalid proxy URL: {e}") from e

    # Validate the URL scheme
    if parsed_url.scheme != "socks5":
        raise ValueError(f"Unsupported proxy scheme: {parsed_url.scheme}. Only 'socks5' is supported.")

    # Extract authentication credentials if present
    username = None
    password = None
    if parsed_url.username:
        username = urllib.parse.unquote(parsed_url.username)
    if parsed_url.password:
        password = urllib.parse.unquote(parsed_url.password)

    # Build the address with default port if not specified
    host = parsed_url.hostname
    if not host:
        raise ValueError("Proxy URL must specify a hostname")
        
    port = parsed_url.port or 9870  # Default SOCKS5 port
    address = f"{host}:{port}"

    return address, username, password


def get_env_or_flag(flag_value: Optional[str], env_var: str) -> Optional[str]:
    """
    Get a configuration value from command-line flag or environment variable.
    
    This utility function implements the common pattern of checking for a value
    in a command-line flag first, and falling back to an environment variable
    if the flag is not provided. This allows users to configure the application
    either through command-line arguments or environment variables.
    
    Args:
        flag_value: The value provided via command-line flag (may be None)
        env_var: The name of the environment variable to check as fallback
        
    Returns:
        The configuration value from the flag if provided, otherwise from
        the environment variable, or None if neither is set
        
    Examples:
        >>> os.environ['LINKSOCKS_TOKEN'] = 'env-token'
        >>> get_env_or_flag(None, 'LINKSOCKS_TOKEN')
        'env-token'
        
        >>> get_env_or_flag('flag-token', 'LINKSOCKS_TOKEN')
        'flag-token'
        
        >>> get_env_or_flag(None, 'NONEXISTENT_VAR')
        None
        
    Note:
        Command-line flags always take precedence over environment variables.
        This allows users to override environment settings on a per-invocation basis.
    """
    if flag_value:
        return flag_value
    return os.getenv(env_var)


def validate_required_token(token: Optional[str], env_var: str = "LINKSOCKS_TOKEN") -> str:
    """
    Validate that a required authentication token is provided.
    
    This function checks that an authentication token is available either from
    a command-line flag or environment variable, and raises a descriptive error
    if neither is provided.
    
    Args:
        token: The token value from command-line flag (may be None)
        env_var: The environment variable name to check (default: "LINKSOCKS_TOKEN")
        
    Returns:
        The validated token string
        
    Raises:
        ValueError: If no token is provided via flag or environment variable
        
    Example:
        >>> validate_required_token("my-token")
        "my-token"
        
        >>> validate_required_token(None)  # Will check LINKSOCKS_TOKEN env var
        ValueError: Token is required. Provide via --token or LINKSOCKS_TOKEN environment variable.
    """
    actual_token = get_env_or_flag(token, env_var)
    if not actual_token:
        raise ValueError(f"Token is required. Provide via --token or {env_var} environment variable.")
    return actual_token