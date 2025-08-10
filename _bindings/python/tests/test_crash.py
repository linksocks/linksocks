"""
Crash stress tests for linksocks C extension bindings.

This module tests various edge cases and invalid inputs that could potentially
cause the C extension to crash rather than raise proper Python exceptions.
Tests are organized by method/class to systematically test each parameter.
"""

import asyncio
from typing import Any, List

from .utils import get_free_port


# Test data generators for different types of invalid inputs
class InvalidInputs:
    """Generate various types of invalid inputs that might crash C extension"""
    
    @staticmethod
    def get_invalid_strings() -> List[Any]:
        """Invalid string inputs that might cause buffer overflows or crashes"""
        return [
            None,  # None instead of string
            123,   # int instead of string
            [],    # list instead of string
            {},    # dict instead of string
            b"bytes",  # bytes instead of string
            "\x00" * 1000,  # null bytes
            "a" * 100000,   # extremely long string
            "\xff" * 1000,  # invalid UTF-8
            "",     # empty string (edge case)
            "\x00",  # single null byte
            "\x00\x01\x02",  # mixed control chars
            "A" * 10000000,  # very large string that might cause memory issues
            "\ud800",  # invalid Unicode surrogate
            "\uffff",  # Unicode replacement character
        ]
    
    @staticmethod
    def get_invalid_ints() -> List[Any]:
        """Invalid integer inputs"""
        return [
            "not_an_int",  # string instead of int
            [],            # list instead of int
            {},            # dict instead of int
            None,          # None instead of int
            3.14,          # float instead of int
            -1,            # negative (might be invalid for ports)
            0,             # zero (might be invalid)
            65536,         # port out of range
            2**31,         # very large int
            -2**31,        # very negative int
            2**63,         # int64 overflow
            float('inf'),  # infinity
            float('nan'),  # NaN
        ]
    
    @staticmethod
    def get_invalid_bools() -> List[Any]:
        """Invalid boolean inputs"""
        return [
            "true",   # string instead of bool
            1,        # int instead of bool
            [],       # list instead of bool
            {},       # dict instead of bool
            None,     # None instead of bool
        ]
    
    @staticmethod
    def get_complex_invalid_objects() -> List[Any]:
        """Complex objects that might cause issues in C extension"""
        # Create circular reference
        circular_list = []
        circular_list.append(circular_list)
        
        # Create deeply nested structure
        deep_nested = {"a": {"b": {"c": {"d": "e" * 1000}}}}
        
        return [
            circular_list,     # circular reference
            deep_nested,       # deeply nested dict
            lambda x: x,       # function object
            type,              # type object
            object(),          # arbitrary object
            [1, 2, 3] * 10000, # very large list
        ]
    
    @staticmethod
    def get_invalid_durations() -> List[Any]:
        """Invalid duration inputs"""
        return [
            [],              # list instead of duration
            {},              # dict instead of duration
            "invalid",       # invalid duration string
            -1,              # negative duration
            float('inf'),    # infinite duration
            float('nan'),    # NaN duration
            "1x",            # malformed duration string
            "1.5.5s",        # malformed duration string
            2**63,           # overflow duration
        ]


class TestServerCrash:
    """Test Server class methods for potential crashes"""
    
    def test_server_init_invalid_ws_port(self):
        """Test Server.__init__ with invalid ws_port parameter"""
        from linksocks import Server
        
        invalid_ints = InvalidInputs.get_invalid_ints()
        
        for i, ws_port in enumerate(invalid_ints):
            try:
                print(f"Testing ws_port #{i}: {repr(ws_port)}")
                server = Server(ws_port=ws_port)
                try:
                    server.close()
                except:
                    pass
                print(f"ws_port #{i} passed")
            except Exception as e:
                print(f"ws_port #{i} raised exception: {e}")
                pass
    
    def test_server_init_invalid_buffer_size_0(self):
        """Test Server.__init__ with buffer_size='not_an_int'"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size="not_an_int")
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_1(self):
        """Test Server.__init__ with buffer_size=[]"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=[])
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_2(self):
        """Test Server.__init__ with buffer_size={}"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size={})
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_3(self):
        """Test Server.__init__ with buffer_size=None"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=None)
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_4(self):
        """Test Server.__init__ with buffer_size=-1"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=-1)
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_5(self):
        """Test Server.__init__ with buffer_size=0"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=0)
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_6(self):
        """Test Server.__init__ with buffer_size=2**31"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=2**31)
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_7(self):
        """Test Server.__init__ with buffer_size=float('inf')"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=float('inf'))
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_buffer_size_8(self):
        """Test Server.__init__ with buffer_size=float('nan')"""
        from linksocks import Server
        try:
            server = Server(ws_port=get_free_port(), buffer_size=float('nan'))
            server.close()
        except Exception:
            pass
    
    def test_server_init_invalid_ws_host(self):
        """Test Server.__init__ with invalid ws_host parameter"""
        from linksocks import Server
        
        invalid_strings = InvalidInputs.get_invalid_strings()
        
        for i, ws_host in enumerate(invalid_strings):
            try:
                print(f"Testing ws_host #{i}: {repr(ws_host)}")
                server = Server(ws_port=get_free_port(), ws_host=ws_host)
                try:
                    server.close()
                except:
                    pass
                print(f"ws_host #{i} passed")
            except Exception as e:
                print(f"ws_host #{i} raised exception: {e}")
                pass
    
    def test_server_init_invalid_api_key(self):
        """Test Server.__init__ with invalid api_key parameter"""
        from linksocks import Server
        
        invalid_strings = InvalidInputs.get_invalid_strings()
        
        for i, api_key in enumerate(invalid_strings):
            try:
                print(f"Testing api_key #{i}: {repr(api_key)}")
                server = Server(ws_port=get_free_port(), api_key=api_key)
                try:
                    server.close()
                except:
                    pass
                print(f"api_key #{i} passed")
            except Exception as e:
                print(f"api_key #{i} raised exception: {e}")
                pass
    
    def test_add_forward_token_invalid_params(self):
        """Test Server.add_forward_token with invalid parameters"""
        from linksocks import Server
        
        try:
            server = Server(ws_port=get_free_port())
            
            invalid_strings = InvalidInputs.get_invalid_strings()
            
            for token in invalid_strings:
                try:
                    result = server.add_forward_token(token)
                    # Should return a string or raise exception, not crash
                    assert isinstance(result, str) or result is None
                except Exception:
                    # Python exceptions are acceptable
                    pass
                    
        except Exception:
            # Server creation might fail, that's ok
            pass
        finally:
            try:
                server.close()
            except:
                pass
    
    def test_add_reverse_token_invalid_params(self):
        """Test Server.add_reverse_token with invalid parameters"""
        from linksocks import Server
        
        try:
            server = Server(ws_port=get_free_port())
            
            invalid_strings = InvalidInputs.get_invalid_strings()
            invalid_ints = InvalidInputs.get_invalid_ints()
            invalid_bools = InvalidInputs.get_invalid_bools()
            
            test_cases = [
                # token parameter
                *[{"token": val} for val in invalid_strings],
                
                # port parameter
                *[{"port": val} for val in invalid_ints],
                
                # username parameter
                *[{"username": val} for val in invalid_strings],
                
                # password parameter
                *[{"password": val} for val in invalid_strings],
                
                # allow_manage_connector parameter
                *[{"allow_manage_connector": val} for val in invalid_bools],
            ]
            
            for kwargs in test_cases:
                try:
                    result = server.add_reverse_token(**kwargs)
                    # Should return ReverseTokenResult or raise exception
                    assert hasattr(result, 'token') and hasattr(result, 'port')
                except Exception:
                    # Python exceptions are acceptable
                    pass
                    
        except Exception:
            # Server creation might fail, that's ok
            pass
        finally:
            try:
                server.close()
            except:
                pass
    
    def test_add_connector_token_invalid_params(self):
        """Test Server.add_connector_token with invalid parameters"""
        from linksocks import Server
        
        try:
            server = Server(ws_port=get_free_port())
            
            invalid_strings = InvalidInputs.get_invalid_strings()
            
            for connector_token in invalid_strings:
                for reverse_token in invalid_strings:
                    try:
                        result = server.add_connector_token(connector_token, reverse_token)
                        # Should return string or raise exception
                        assert isinstance(result, str) or result is None
                    except Exception:
                        # Python exceptions are acceptable
                        pass
                        
        except Exception:
            # Server creation might fail, that's ok
            pass
        finally:
            try:
                server.close()
            except:
                pass
    
    def test_remove_token_invalid_params(self):
        """Test Server.remove_token with invalid parameters"""
        from linksocks import Server
        
        try:
            server = Server(ws_port=get_free_port())
            
            invalid_strings = InvalidInputs.get_invalid_strings()
            
            for token in invalid_strings:
                try:
                    result = server.remove_token(token)
                    # Should return bool or raise exception
                    assert isinstance(result, bool) or result is None
                except Exception:
                    # Python exceptions are acceptable
                    pass
                    
        except Exception:
            # Server creation might fail, that's ok
            pass
        finally:
            try:
                server.close()
            except:
                pass
    
    def test_wait_ready_invalid_params(self):
        """Test Server.wait_ready with invalid parameters"""
        from linksocks import Server
        
        try:
            server = Server(ws_port=get_free_port())
            
            invalid_durations = InvalidInputs.get_invalid_durations()
            
            for timeout in invalid_durations:
                try:
                    # This might block, so use a short timeout
                    server.wait_ready(timeout)
                except Exception:
                    # Python exceptions are acceptable
                    pass
                    
        except Exception:
            # Server creation might fail, that's ok
            pass
        finally:
            try:
                server.close()
            except:
                pass


class TestClientCrash:
    """Test Client class methods for potential crashes"""
    
    def test_client_init_invalid_token(self):
        """Test Client.__init__ with invalid token parameter"""
        from linksocks import Client
        
        invalid_strings = InvalidInputs.get_invalid_strings()
        
        # Test token parameter (required) - each one individually
        for i, token in enumerate(invalid_strings):
            try:
                print(f"Testing token #{i}: {repr(token)}")
                client = Client(token)
                try:
                    client.close()
                except:
                    pass
                print(f"Token #{i} passed")
            except Exception as e:
                # Python exceptions are acceptable
                print(f"Token #{i} raised exception: {e}")
                pass
    
    def test_client_init_invalid_ws_url(self):
        """Test Client.__init__ with invalid ws_url parameter"""
        from linksocks import Client
        
        invalid_strings = InvalidInputs.get_invalid_strings()
        valid_token = "test_token"
        
        for i, ws_url in enumerate(invalid_strings):
            try:
                print(f"Testing ws_url #{i}: {repr(ws_url)}")
                client = Client(valid_token, ws_url=ws_url)
                try:
                    client.close()
                except:
                    pass
                print(f"ws_url #{i} passed")
            except Exception as e:
                print(f"ws_url #{i} raised exception: {e}")
                pass
    
    def test_client_init_invalid_socks_port(self):
        """Test Client.__init__ with invalid socks_port parameter"""
        from linksocks import Client
        
        invalid_ints = InvalidInputs.get_invalid_ints()
        valid_token = "test_token"
        
        for i, socks_port in enumerate(invalid_ints):
            try:
                print(f"Testing socks_port #{i}: {repr(socks_port)}")
                client = Client(valid_token, socks_port=socks_port)
                try:
                    client.close()
                except:
                    pass
                print(f"socks_port #{i} passed")
            except Exception as e:
                print(f"socks_port #{i} raised exception: {e}")
                pass
    
    def test_client_init_invalid_buffer_size_0(self):
        """Test Client.__init__ with buffer_size='not_an_int'"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size="not_an_int")
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_1(self):
        """Test Client.__init__ with buffer_size=[]"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=[])
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_2(self):
        """Test Client.__init__ with buffer_size={}"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size={})
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_3(self):
        """Test Client.__init__ with buffer_size=None"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=None)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_4(self):
        """Test Client.__init__ with buffer_size=3.14"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=3.14)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_5(self):
        """Test Client.__init__ with buffer_size=-1"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=-1)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_6(self):
        """Test Client.__init__ with buffer_size=0"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=0)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_7(self):
        """Test Client.__init__ with buffer_size=2**31"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=2**31)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_8(self):
        """Test Client.__init__ with buffer_size=float('inf')"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=float('inf'))
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_buffer_size_9(self):
        """Test Client.__init__ with buffer_size=float('nan')"""
        from linksocks import Client
        try:
            client = Client("test_token", buffer_size=float('nan'))
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_0(self):
        """Test Client.__init__ with threads=None"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=None)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_1(self):
        """Test Client.__init__ with threads='not_an_int'"""
        from linksocks import Client
        try:
            client = Client("test_token", threads="not_an_int")
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_2(self):
        """Test Client.__init__ with threads=[]"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=[])
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_3(self):
        """Test Client.__init__ with threads={}"""
        from linksocks import Client
        try:
            client = Client("test_token", threads={})
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_4(self):
        """Test Client.__init__ with threads=3.14"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=3.14)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_5(self):
        """Test Client.__init__ with threads=-1"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=-1)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_6(self):
        """Test Client.__init__ with threads=0"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=0)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_7(self):
        """Test Client.__init__ with threads=2**31 (large int)"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=2**31)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_8(self):
        """Test Client.__init__ with threads=-2**31 (very negative)"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=-2**31)
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_9(self):
        """Test Client.__init__ with threads=float('inf')"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=float('inf'))
            client.close()
        except Exception:
            pass
    
    def test_client_init_invalid_threads_10(self):
        """Test Client.__init__ with threads=float('nan')"""
        from linksocks import Client
        try:
            client = Client("test_token", threads=float('nan'))
            client.close()
        except Exception:
            pass
    
    def test_wait_ready_invalid_params(self):
        """Test Client.wait_ready with invalid parameters"""
        from linksocks import Client
        
        try:
            client = Client("test_token")
            
            invalid_durations = InvalidInputs.get_invalid_durations()
            
            for timeout in invalid_durations:
                try:
                    client.wait_ready(timeout)
                except Exception:
                    # Python exceptions are acceptable
                    pass
                    
        except Exception:
            # Client creation might fail, that's ok
            pass
        finally:
            try:
                client.close()
            except:
                pass
    
    def test_add_connector_invalid_params(self):
        """Test Client.add_connector with invalid parameters"""
        from linksocks import Client
        
        try:
            client = Client("test_token")
            
            invalid_strings = InvalidInputs.get_invalid_strings()
            
            for connector_token in invalid_strings:
                try:
                    result = client.add_connector(connector_token)
                    # Should return string or raise exception
                    assert isinstance(result, str) or result is None
                except Exception:
                    # Python exceptions are acceptable
                    pass
                    
        except Exception:
            # Client creation might fail, that's ok
            pass
        finally:
            try:
                client.close()
            except:
                pass
    
    def test_property_access_edge_cases(self):
        """Test property access that might cause crashes"""
        from linksocks import Client
        
        try:
            client = Client("test_token")
            
            # Test property access when client might be in invalid state
            try:
                _ = client.is_connected
            except Exception:
                pass
                
            try:
                _ = client.socks_port
            except Exception:
                pass
                
        except Exception:
            # Client creation might fail, that's ok
            pass
        finally:
            try:
                client.close()
            except:
                pass


class TestAsyncMethodsCrash:
    """Test async methods for potential crashes"""
    
    def test_server_async_methods_invalid_params(self):
        """Test Server async methods with invalid parameters"""
        from linksocks import Server
        
        async def _test_async():
            try:
                server = Server(ws_port=get_free_port())
                
                invalid_strings = InvalidInputs.get_invalid_strings()
                invalid_durations = InvalidInputs.get_invalid_durations()
                
                # Test async_add_forward_token
                for token in invalid_strings[:3]:  # Limit to avoid too many tests
                    try:
                        result = await server.async_add_forward_token(token)
                        assert isinstance(result, str) or result is None
                    except Exception:
                        pass
                
                # Test async_wait_ready
                for timeout in invalid_durations[:3]:
                    try:
                        await server.async_wait_ready(timeout)
                    except Exception:
                        pass
                        
            except Exception:
                pass
            finally:
                try:
                    await server.async_close()
                except:
                    pass
        
        asyncio.run(asyncio.wait_for(_test_async(), 30))
    
    def test_client_async_methods_invalid_params(self):
        """Test Client async methods with invalid parameters"""
        from linksocks import Client
        
        async def _test_async():
            try:
                client = Client("test_token")
                
                invalid_strings = InvalidInputs.get_invalid_strings()
                invalid_durations = InvalidInputs.get_invalid_durations()
                
                # Test async_wait_ready
                for timeout in invalid_durations[:3]:  # Limit to avoid too many tests
                    try:
                        await client.async_wait_ready(timeout)
                    except Exception:
                        pass
                
                # Test async_add_connector
                for connector_token in invalid_strings[:3]:
                    try:
                        result = await client.async_add_connector(connector_token)
                        assert isinstance(result, str) or result is None
                    except Exception:
                        pass
                        
            except Exception:
                pass
            finally:
                try:
                    await client.async_close()
                except:
                    pass
        
        asyncio.run(asyncio.wait_for(_test_async(), 30))


class TestEdgeCasesAndCleanup:
    """Test edge cases and cleanup scenarios that might cause crashes"""
    
    def test_double_close(self):
        """Test calling close() multiple times"""
        from linksocks import Server, Client
        
        # Test Server double close
        try:
            server = Server(ws_port=get_free_port())
            server.close()
            server.close()  # Should not crash
            server.close()  # Should not crash
        except Exception:
            pass
        
        # Test Client double close
        try:
            client = Client("test_token")
            client.close()
            client.close()  # Should not crash
            client.close()  # Should not crash
        except Exception:
            pass
    
    def test_use_after_close(self):
        """Test using objects after close() - should not crash"""
        from linksocks import Server, Client
        
        # Test Server use after close
        try:
            server = Server(ws_port=get_free_port())
            server.close()
            
            # These should raise exceptions, not crash
            try:
                server.add_forward_token()
            except Exception:
                pass
                
            try:
                server.wait_ready()
            except Exception:
                pass
                
        except Exception:
            pass
        
        # Test Client use after close
        try:
            client = Client("test_token")
            client.close()
            
            # These should raise exceptions, not crash
            try:
                client.add_connector("")
            except Exception:
                pass
                
            try:
                client.wait_ready()
            except Exception:
                pass
                
            try:
                _ = client.is_connected
            except Exception:
                pass
                
        except Exception:
            pass
    
    def test_context_manager_edge_cases(self):
        """Test context manager edge cases"""
        from linksocks import Server, Client
        
        # Test Server context manager with exceptions
        try:
            with Server(ws_port=get_free_port()) as server:
                # Simulate an error inside context
                raise RuntimeError("Test error")
        except RuntimeError:
            # Should not crash during cleanup
            pass
        except Exception:
            pass
        
        # Test Client context manager with exceptions
        try:
            with Client("test_token") as client:
                # Simulate an error inside context
                raise RuntimeError("Test error")
        except RuntimeError:
            # Should not crash during cleanup
            pass
        except Exception:
            pass
    
    def test_async_context_manager_edge_cases(self):
        """Test async context manager edge cases"""
        from linksocks import Server, Client
        
        async def _test_async():
            # Test Server async context manager with exceptions
            try:
                async with Server(ws_port=get_free_port()) as server:
                    # Simulate an error inside context
                    raise RuntimeError("Test error")
            except RuntimeError:
                # Should not crash during cleanup
                pass
            except Exception:
                pass
            
            # Test Client async context manager with exceptions
            try:
                async with Client("test_token") as client:
                    # Simulate an error inside context
                    raise RuntimeError("Test error")
            except RuntimeError:
                # Should not crash during cleanup
                pass
            except Exception:
                pass
        
        asyncio.run(asyncio.wait_for(_test_async(), 30))
    
    def test_logger_registry_race_condition(self):
        """Test race condition in logger registry during rapid create/destroy"""
        import threading
        import time
        from linksocks import Server, Client
        
        def create_destroy_server():
            for i in range(50):
                try:
                    server = Server(ws_port=get_free_port())
                    # Force immediate destruction without proper cleanup
                    del server
                except Exception:
                    pass
        
        def create_destroy_client():
            for i in range(50):
                try:
                    client = Client("token")
                    # Force immediate destruction without proper cleanup
                    del client
                except Exception:
                    pass
        
        # Create multiple threads to trigger race conditions
        threads = []
        for _ in range(5):
            threads.append(threading.Thread(target=create_destroy_server))
            threads.append(threading.Thread(target=create_destroy_client))
        
        # Start all threads simultaneously
        for t in threads:
            t.start()
        
        # Wait for completion
        for t in threads:
            t.join()
    
    def test_log_buffer_overflow_attack(self):
        """Test log buffer overflow by flooding with massive log entries"""
        from linksocks import Server
        import threading
        import time
        
        def flood_logs():
            for i in range(1000):
                try:
                    server = Server(ws_port=get_free_port())
                    # Generate massive log entries that might overflow buffers
                    huge_token = "A" * 100000  # 100KB token
                    server.add_forward_token(huge_token)
                    # Don't close immediately to let logs accumulate
                    time.sleep(0.001)
                    server.close()
                except Exception:
                    pass
        
        # Multiple threads flooding logs simultaneously
        threads = [threading.Thread(target=flood_logs) for _ in range(10)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_log_channel_slice_corruption(self):
        """Test corruption of log notification channels slice"""
        from linksocks import Server
        import linksocks
        import threading
        
        def corrupt_channels():
            # Try to corrupt the log channels during operations
            try:
                server = Server(ws_port=get_free_port())
                # Trigger log operations
                server.add_forward_token()
                
                # Try to corrupt Go's internal channel slice
                # This should trigger the slice corruption in WaitForLogEntries
                for i in range(100):
                    try:
                        # Rapid creation/destruction might corrupt channel indices
                        temp_server = Server(ws_port=get_free_port())
                        temp_server.close()
                    except:
                        pass
                
                server.close()
            except Exception:
                pass
        
        # Multiple threads to increase race condition chances
        threads = [threading.Thread(target=corrupt_channels) for _ in range(5)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_buffer_writer_race_condition(self):
        """Test race condition in bufferWriter.Write method"""
        from linksocks import Server, Client
        import threading
        
        def stress_buffer_writer():
            for i in range(200):
                try:
                    # Rapid server/client creation to stress buffer writer
                    server = Server(ws_port=get_free_port())
                    client = Client("token" + str(i))
                    
                    # Operations that trigger logging
                    server.add_forward_token()
                    client.add_connector("")
                    
                    # Close in different order to trigger race
                    if i % 2 == 0:
                        server.close()
                        client.close()
                    else:
                        client.close()
                        server.close()
                except Exception:
                    pass
        
        # High concurrency to trigger race in buffer writer
        threads = [threading.Thread(target=stress_buffer_writer) for _ in range(8)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_event_driven_logging_corruption(self):
        """Test corruption in event-driven logging system"""
        from linksocks import Server
        import threading
        import gc
        
        def spam_logger_operations():
            for i in range(100):
                try:
                    server = Server(ws_port=get_free_port())
                    # Trigger logging operations
                    server.add_forward_token()
                    # Force garbage collection during active logging
                    gc.collect()
                    server.close()
                except Exception:
                    pass
        
        # Multiple threads accessing logging system
        threads = [threading.Thread(target=spam_logger_operations) for _ in range(3)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_context_cancel_during_operations(self):
        """Test canceling context during active operations"""
        from linksocks import Server, Client
        import threading
        
        def test_server_context_cancel():
            try:
                server = Server(ws_port=get_free_port())
                # Access private context and cancel it during operations
                if hasattr(server, '_ctx') and server._ctx:
                    server._ctx.Cancel()
                # Try to use server after context cancel
                server.add_forward_token()
                server.wait_ready()
            except Exception:
                pass
            finally:
                try:
                    server.close()
                except:
                    pass
        
        def test_client_context_cancel():
            try:
                client = Client("token")
                # Access private context and cancel it during operations
                if hasattr(client, '_ctx') and client._ctx:
                    client._ctx.Cancel()
                # Try to use client after context cancel
                client.add_connector("")
                client.wait_ready()
            except Exception:
                pass
            finally:
                try:
                    client.close()
                except:
                    pass
        
        # Run in parallel to increase chance of race conditions
        threads = [
            threading.Thread(target=test_server_context_cancel),
            threading.Thread(target=test_client_context_cancel)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_managed_logger_corruption(self):
        """Test corruption of managed logger during operations"""
        from linksocks import Server, Client
        
        try:
            server = Server(ws_port=get_free_port())
            # Corrupt the managed logger reference
            if hasattr(server, '_managed_logger'):
                server._managed_logger.py_logger = None
                server._managed_logger.logger_id = None
                server._managed_logger.go_logger = None
            # Try to use server with corrupted logger
            server.add_forward_token()
        except Exception:
            pass
        finally:
            try:
                server.close()
            except:
                pass
        
        try:
            client = Client("token")
            # Corrupt the managed logger reference
            if hasattr(client, '_managed_logger'):
                client._managed_logger.py_logger = None
                client._managed_logger.logger_id = None
                client._managed_logger.go_logger = None
            # Try to use client with corrupted logger
            client.add_connector("")
        except Exception:
            pass
        finally:
            try:
                client.close()
            except:
                pass
    
    def test_circular_reference_in_go_objects(self):
        """Test circular references between Go and Python objects"""
        from linksocks import Server, Client
        
        # Create circular reference with server
        try:
            server = Server(ws_port=get_free_port())
            # Create circular reference
            server._self_ref = server
            server._raw._py_ref = server
            # Force garbage collection
            import gc
            gc.collect()
            server.add_forward_token()
        except Exception:
            pass
        finally:
            try:
                server.close()
            except:
                pass
        
        # Create circular reference with client
        try:
            client = Client("token")
            # Create circular reference
            client._self_ref = client
            client._raw._py_ref = client
            # Force garbage collection
            import gc
            gc.collect()
            client.add_connector("")
        except Exception:
            pass
        finally:
            try:
                client.close()
            except:
                pass
    
    def test_invalid_go_object_access(self):
        """Test accessing Go objects after they've been freed"""
        from linksocks import Server, Client
        
        # Test server Go object access after close
        try:
            server = Server(ws_port=get_free_port())
            raw_ref = server._raw
            server.close()
            # Try to access Go object after close
            raw_ref.AddForwardToken("")
            raw_ref.WaitReady()
        except Exception:
            pass
        
        # Test client Go object access after close
        try:
            client = Client("token")
            raw_ref = client._raw
            client.close()
            # Try to access Go object after close
            raw_ref.AddConnector("")
            raw_ref.WaitReady()
        except Exception:
            pass
    
    def test_logger_registry_corruption(self):
        """Test corruption of global logger registry"""
        from linksocks import Server
        import linksocks
        
        try:
            # Corrupt the global logger registry
            if hasattr(linksocks, '_logger_registry'):
                original_registry = linksocks._logger_registry.copy()
                # Inject invalid entries
                linksocks._logger_registry['invalid'] = None
                linksocks._logger_registry[123] = "not_a_logger"
                linksocks._logger_registry[None] = object()
            
            # Try to create server with corrupted registry
            server = Server(ws_port=get_free_port())
            server.add_forward_token()
            server.close()
            
            # Restore registry
            if hasattr(linksocks, '_logger_registry'):
                linksocks._logger_registry.clear()
                linksocks._logger_registry.update(original_registry)
        except Exception:
            pass
    
    def test_event_listener_corruption(self):
        """Test corruption of event listener system"""
        from linksocks import Server
        import linksocks
        
        try:
            # Corrupt the log listeners list
            if hasattr(linksocks, '_log_listeners'):
                original_listeners = linksocks._log_listeners.copy()
                # Inject invalid listeners
                linksocks._log_listeners.append(None)
                linksocks._log_listeners.append("not_callable")
                linksocks._log_listeners.append(123)
                
                # Add a listener that raises exceptions
                def bad_listener(entries):
                    raise RuntimeError("Bad listener")
                linksocks._log_listeners.append(bad_listener)
            
            # Try to create server with corrupted listeners
            server = Server(ws_port=get_free_port())
            server.add_forward_token()
            server.close()
            
            # Restore listeners
            if hasattr(linksocks, '_log_listeners'):
                linksocks._log_listeners.clear()
                linksocks._log_listeners.extend(original_listeners)
        except Exception:
            pass
    
    def test_go_duration_overflow(self):
        """Test Go duration calculations with extreme values"""
        from linksocks import Server, Client
        
        # Test with values that might cause integer overflow in Go
        extreme_values = [
            2**63 - 1,  # max int64
            2**62,      # large positive
            -2**62,     # large negative
            float('inf'),  # infinity
            float('-inf'), # negative infinity
            1e100,      # very large float
            1e-100,     # very small float
        ]
        
        for value in extreme_values:
            try:
                server = Server(
                    ws_port=get_free_port(),
                    channel_timeout=value,
                    connect_timeout=value
                )
                server.wait_ready(timeout=value)
                server.close()
            except Exception:
                pass
            
            try:
                client = Client(
                    "token",
                    reconnect_delay=value,
                    channel_timeout=value,
                    connect_timeout=value
                )
                client.wait_ready(timeout=value)
                client.close()
            except Exception:
                pass
    
    def test_go_string_boundary_corruption(self):
        """Test string boundary corruption between Go and Python"""
        from linksocks import Server, Client
        
        # Test with strings that might cause buffer overruns
        dangerous_strings = [
            "\x00" + "A" * 1000,  # null byte followed by data
            "A" * 1000 + "\x00" + "B" * 1000,  # embedded null
            "\xff\xfe" + "A" * 1000,  # BOM + data
            "\x80" * 1000,  # high-bit chars
            "🚀" * 1000,  # unicode that expands in UTF-8
        ]
        
        for dangerous_str in dangerous_strings:
            try:
                server = Server(ws_port=get_free_port(), api_key=dangerous_str)
                server.add_forward_token(dangerous_str)
                server.close()
            except Exception:
                pass
            
            try:
                client = Client(dangerous_str, ws_url="ws://localhost:8080")
                client.add_connector(dangerous_str)
                client.close()
            except Exception:
                pass
    
    def test_async_context_corruption(self):
        """Test corruption of async context during operations"""
        import asyncio
        from linksocks import Server, Client
        
        async def corrupt_async_context():
            # Test server async context corruption
            try:
                server = Server(ws_port=get_free_port())
                # Start async wait
                task = asyncio.create_task(server.async_wait_ready(timeout=10))
                # Corrupt the context while waiting
                if hasattr(server, '_ctx') and server._ctx:
                    server._ctx = None
                await task
                await server.async_close()
            except Exception:
                pass
            
            # Test client async context corruption
            try:
                client = Client("token")
                # Start async wait
                task = asyncio.create_task(client.async_wait_ready(timeout=10))
                # Corrupt the context while waiting
                if hasattr(client, '_ctx') and client._ctx:
                    client._ctx = None
                await task
                await client.async_close()
            except Exception:
                pass
        
        asyncio.run(corrupt_async_context())
    
    def test_go_panic_recovery(self):
        """Test scenarios that might cause Go panics"""
        from linksocks import Server, Client
        
        # Test with extreme port numbers that might cause panics
        extreme_ports = [-1, 0, 65536, 100000, 2**16, 2**31-1]
        
        for port in extreme_ports:
            try:
                server = Server(ws_port=port)
                server.wait_ready(timeout=0.1)
                server.close()
            except Exception:
                pass
        
        # Test with malformed URLs that might cause panics
        malformed_urls = [
            "://invalid",
            "ws://",
            "ws://\x00",
            "ws://localhost:-1",
            "ws://localhost:999999",
            "ws://\xff\xff",
            "ws://" + "A" * 10000,
        ]
        
        for url in malformed_urls:
            try:
                client = Client("token", ws_url=url)
                client.wait_ready(timeout=0.1)
                client.close()
            except Exception:
                pass
    
    def test_memory_corruption_via_options(self):
        """Test memory corruption through option structures"""
        from linksocks import Server, Client
        
        # Create server with many options to stress option parsing
        try:
            server = Server(
                ws_host="\x00" * 100,
                ws_port=get_free_port(),
                socks_host="\xff" * 100,
                buffer_size=2**31-1,
                api_key="A" * 10000,
                upstream_proxy="http://\x00:80",
                upstream_username="user\x00name",
                upstream_password="pass\x00word"
            )
            server.close()
        except Exception:
            pass
        
        # Create client with many options to stress option parsing
        try:
            client = Client(
                "token\x00token",
                ws_url="ws://localhost\x00:8080",
                socks_host="\xff" * 100,
                socks_username="user\x00name",
                socks_password="pass\x00word",
                buffer_size=2**31-1,
                threads=-1,
                upstream_proxy="http://\x00:80",
                upstream_username="user\x00name",
                upstream_password="pass\x00word"
            )
            client.close()
        except Exception:
            pass
    
    def test_concurrent_context_operations(self):
        """Test concurrent context operations that might cause races"""
        import threading
        from linksocks import Server, Client
        
        def stress_server_context():
            server = None
            try:
                server = Server(ws_port=get_free_port())
                # Rapid context operations
                for _ in range(100):
                    try:
                        server.wait_ready(timeout=0.001)
                    except:
                        pass
            except Exception:
                pass
            finally:
                if server:
                    try:
                        server.close()
                    except:
                        pass
        
        def stress_client_context():
            client = None
            try:
                client = Client("token")
                # Rapid context operations
                for _ in range(100):
                    try:
                        client.wait_ready(timeout=0.001)
                    except:
                        pass
            except Exception:
                pass
            finally:
                if client:
                    try:
                        client.close()
                    except:
                        pass
        
        # Run multiple threads concurrently
        threads = []
        for _ in range(5):
            threads.append(threading.Thread(target=stress_server_context))
            threads.append(threading.Thread(target=stress_client_context))
        
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_slice_bounds_corruption(self):
        """Test Go slice bounds corruption in channel management"""
        from linksocks import Server
        import threading
        import time
        
        def trigger_slice_corruption():
            servers = []
            try:
                # Create many servers rapidly to fill channel slice
                for i in range(50):
                    server = Server(ws_port=get_free_port())
                    servers.append(server)
                    server.add_forward_token()
                
                # Close them in reverse order to corrupt slice indices
                for server in reversed(servers):
                    server.close()
                    time.sleep(0.001)  # Small delay to trigger race
            except Exception:
                pass
        
        # Multiple threads to increase corruption chance
        threads = [threading.Thread(target=trigger_slice_corruption) for _ in range(3)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    
    def test_go_panic_via_nil_pointer(self):
        """Test Go panic via nil pointer dereference"""
        from linksocks import Server, Client
        
        # Test server with corrupted internal state
        try:
            server = Server(ws_port=get_free_port())
            # Corrupt internal Go objects to trigger nil pointer access
            if hasattr(server, '_raw'):
                # Try to trigger operations on potentially nil Go objects
                server._raw = None  # This might cause nil pointer panic
                server.add_forward_token()  # Should panic if _raw is accessed
        except Exception:
            pass
        
        # Test client with corrupted internal state  
        try:
            client = Client("token")
            if hasattr(client, '_raw'):
                client._raw = None  # This might cause nil pointer panic
                client.add_connector("")  # Should panic if _raw is accessed
        except Exception:
            pass
    
    def test_double_free_corruption(self):
        """Test double-free corruption in Go objects"""
        from linksocks import Server, Client
        import gc
        
        # Force double-free scenario
        try:
            server = Server(ws_port=get_free_port())
            raw_obj = server._raw
            
            # Close server (frees Go object)
            server.close()
            
            # Force garbage collection
            gc.collect()
            
            # Try to use raw object after free (should crash)
            raw_obj.AddForwardToken("")
            raw_obj.Close()  # Double free
        except Exception:
            pass
        
        try:
            client = Client("token")
            raw_obj = client._raw
            
            # Close client (frees Go object)
            client.close()
            
            # Force garbage collection
            gc.collect()
            
            # Try to use raw object after free (should crash)
            raw_obj.AddConnector("")
            raw_obj.Close()  # Double free
        except Exception:
            pass
    
    def test_format_string_attack(self):
        """Test format string vulnerabilities in logging"""
        from linksocks import Server, Client
        
        # Format string attack payloads
        format_attacks = [
            "%s%s%s%s%s%s%s%s%s%s",
            "%x%x%x%x%x%x%x%x%x%x", 
            "%n%n%n%n%n%n%n%n%n%n",
            "%.1000000d",
            "%*.*s",
        ]
        
        for attack in format_attacks:
            try:
                server = Server(ws_port=get_free_port(), api_key=attack)
                server.add_forward_token(attack)
                server.close()
            except Exception:
                pass
            
            try:
                client = Client(attack)
                client.add_connector(attack)
                client.close()
            except Exception:
                pass


if __name__ == "__main__":
    # Run basic smoke tests to check for immediate crashes
    print("Running crash stress tests...")
    
    # Test basic instantiation with invalid params
    test_server = TestServerCrash()
    test_server.test_server_init_invalid_params()
    
    test_client = TestClientCrash()
    test_client.test_client_init_invalid_params()
    
    # Test edge cases and advanced crash scenarios
    test_edge = TestEdgeCasesAndCleanup()
    test_edge.test_double_close()
    test_edge.test_use_after_close()
    test_edge.test_logger_registry_race_condition()
    test_edge.test_log_buffer_overflow_attack()
    test_edge.test_log_channel_slice_corruption()
    test_edge.test_buffer_writer_race_condition()
    test_edge.test_event_driven_logging_corruption()
    test_edge.test_context_cancel_during_operations()
    test_edge.test_managed_logger_corruption()
    test_edge.test_circular_reference_in_go_objects()
    test_edge.test_invalid_go_object_access()
    test_edge.test_logger_registry_corruption()
    test_edge.test_event_listener_corruption()
    test_edge.test_go_duration_overflow()
    test_edge.test_go_string_boundary_corruption()
    test_edge.test_async_context_corruption()
    test_edge.test_go_panic_recovery()
    test_edge.test_memory_corruption_via_options()
    test_edge.test_concurrent_context_operations()
    test_edge.test_slice_bounds_corruption()
    test_edge.test_go_panic_via_nil_pointer()
    test_edge.test_double_free_corruption()
    test_edge.test_format_string_attack()
    
    print("Crash stress tests completed - no crashes detected!")