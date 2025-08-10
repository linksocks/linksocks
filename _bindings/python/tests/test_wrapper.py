import asyncio
import time
import logging
import pytest

from .utils import (
    get_free_port,
    has_ipv6_support,
    async_assert_udp_connection,
    async_assert_web_connection,
    parse_host_port,
)


start_time_limit = 60


def _ws_url(port: int) -> str:
    return f"ws://localhost:{port}"


# ==================== Basic ====================


def test_import_wrapper():
    from wssocks import Server, Client

    assert Server is not None and Client is not None


def test_forward_basic(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_basic(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli:
                await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Token removal ====================


def test_forward_remove_token(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                await async_assert_web_connection(website, socks_port)
                srv.remove_token(token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_remove_token(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli:
                await async_assert_web_connection(website, res.port)
                srv.remove_token(res.token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, res.port, timeout=3)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== IPv6 ====================


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_forward_ipv6(website_v6):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                await async_assert_web_connection(website_v6, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_reverse_ipv6(website_v6):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli:
                await async_assert_web_connection(website_v6, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== UDP ====================


def test_udp_forward_proxy(udp_server):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                await async_assert_udp_connection(udp_server, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy(udp_server):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli:
                await async_assert_udp_connection(udp_server, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_forward_proxy_v6(udp_server_v6):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                await async_assert_udp_connection(udp_server_v6, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_reverse_proxy_v6(udp_server_v6):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli:
                await async_assert_udp_connection(udp_server_v6, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Reconnect ====================


def test_forward_reconnect(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()

        # first server
        async with Server(ws_port=ws_port) as srv1:
            token = await srv1.async_add_forward_token()
            async with Client(
                token,
                ws_url=_ws_url(ws_port),
                socks_port=socks_port,
                reconnect=True,
                reconnect_delay=1,
            ) as cli:
                await async_assert_web_connection(website, socks_port)
                # stop server
                await srv1.async_close()
                await asyncio.sleep(2)
                # start new server same port
                async with Server(ws_port=ws_port) as srv2:
                    await srv2.async_add_forward_token(token)
                    await asyncio.sleep(3)
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 45))


def test_reverse_reconnect(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            # first client
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli1:
                await async_assert_web_connection(website, res.port)

            await asyncio.sleep(2)
            # second client
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli2:
                await asyncio.sleep(2)
                await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 45))


# ==================== Connector ====================


def test_connector(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            # add connector token
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)

            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as rcli:
                async with Client(connector, ws_url=_ws_url(ws_port), socks_port=get_free_port()) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_connector_autonomy(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token(allow_manage_connector=True)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as rcli:
                # client creates connector
                token = await rcli.async_add_connector("CONNECTOR")
                assert token
                # Forward client using connector
                async with Client("CONNECTOR", ws_url=_ws_url(ws_port), socks_port=get_free_port()) as fcli:
                    # reverse should fail due to autonomy
                    with pytest.raises(Exception):
                        await async_assert_web_connection(website, res.port, timeout=3)
                    # forward works
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Threads ====================


def test_client_threads(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, threads=2) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Strict Connection ====================


def test_strict_forward(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, strict_connect=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_strict_reverse(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, strict_connect=True) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_strict_strict_forward(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port, strict_connect=True) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, strict_connect=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_strict_strict_reverse(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, strict_connect=True) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, strict_connect=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_strict_connector(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, strict_connect=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, strict_connect=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), strict_connect=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Mixed Strict ====================


def test_mixed_strict_connector_all_strict(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, strict_connect=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, strict_connect=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), strict_connect=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_strict_connector_server_strict(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, strict_connect=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), strict_connect=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_strict_connector_connector_strict(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as rcli:
                await rcli.async_wait_ready(timeout=start_time_limit)
                with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), strict_connect=True
                ) as fcli:
                    await fcli.async_wait_ready(timeout=start_time_limit)
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_strict_connector_client_strict(website):
    from wssocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, strict_connect=True) as rcli:
                async with Client(connector, ws_url=_ws_url(ws_port), socks_port=get_free_port()) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Stress Tests ====================

def test_tcp_stress_forward(website):
    """TCP stress test with high volume traffic, timing and content verification"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                # High volume concurrent requests
                tasks = []
                start_time = time.time()
                
                async def single_request(req_id):
                    await async_assert_web_connection(website, socks_port)
                    return req_id
                
                # Launch 100 concurrent requests
                for i in range(100):
                    tasks.append(single_request(i))
                
                results = await asyncio.gather(*tasks)
                end_time = time.time()
                
                # Verify all requests completed successfully
                assert len(results) == 100, f"Expected 100 results, got {len(results)}"
                
                # Verify timing (should complete within reasonable time for local server)
                duration = end_time - start_time
                assert duration < 30, f"Stress test took too long: {duration}s"
                
                # Verify no packet loss - all requests returned
                request_ids = [r for r in results]
                assert sorted(request_ids) == list(range(100)), "Missing requests detected"
    
    asyncio.run(asyncio.wait_for(_main(), 60))


def test_udp_stress_forward(udp_server):
    """UDP stress test with high volume traffic, timing and content verification"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port) as cli:
                # High volume UDP packets with content verification
                tasks = []
                start_time = time.time()
                
                async def udp_batch_test(batch_id):
                    # Send multiple UDP packets in this batch
                    for i in range(10):
                        await async_assert_udp_connection(udp_server, socks_port)
                    return batch_id
                
                # Launch 50 batches of 10 UDP requests each (500 total)
                for i in range(50):
                    tasks.append(udp_batch_test(i))
                
                results = await asyncio.gather(*tasks)
                end_time = time.time()
                
                # Verify all batches completed
                assert len(results) == 50, f"Expected 50 batches, got {len(results)}"
                
                # Verify timing
                duration = end_time - start_time
                assert duration < 45, f"UDP stress test took too long: {duration}s"
                
                # Verify no batch loss - all batches returned
                batch_ids = [r for r in results]
                assert sorted(batch_ids) == list(range(50)), "Missing UDP batches detected"
    
    asyncio.run(asyncio.wait_for(_main(), 90))


def test_tcp_stress_reverse(website):
    """TCP stress test for reverse proxy with high volume traffic"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as rcli:
                # High volume concurrent requests through reverse proxy
                tasks = []
                start_time = time.time()
                
                async def single_request(req_id):
                    await async_assert_web_connection(website, res.port)
                    return req_id
                
                # Launch 100 concurrent requests
                for i in range(100):
                    tasks.append(single_request(i))
                
                results = await asyncio.gather(*tasks)
                end_time = time.time()
                
                # Verify all requests completed successfully
                assert len(results) == 100, f"Expected 100 results, got {len(results)}"
                
                # Verify timing
                duration = end_time - start_time
                assert duration < 30, f"Reverse TCP stress test took too long: {duration}s"
                
                # Verify no packet loss
                assert sorted(results) == list(range(100)), "Missing requests in reverse proxy"
    
    asyncio.run(asyncio.wait_for(_main(), 60))


def test_udp_stress_reverse(udp_server):
    """UDP stress test for reverse proxy with high volume traffic"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True) as rcli:
                # Test high volume UDP through reverse proxy
                tasks = []
                start_time = time.time()
                
                async def udp_batch_test(batch_id):
                    # Send multiple UDP packets in this batch
                    for i in range(10):
                        await async_assert_udp_connection(udp_server, res.port)
                    return batch_id
                
                # Launch 50 batches of 10 UDP requests each (500 total)
                for i in range(50):
                    tasks.append(udp_batch_test(i))
                
                results = await asyncio.gather(*tasks)
                end_time = time.time()
                
                # Verify all batches completed
                assert len(results) == 50, f"Expected 50 batches, got {len(results)}"
                
                # Verify timing
                duration = end_time - start_time
                assert duration < 45, f"Reverse UDP stress test took too long: {duration}s"
                
                # Verify no batch loss
                assert sorted(results) == list(range(50)), "Missing batches in reverse UDP proxy"
    
    asyncio.run(asyncio.wait_for(_main(), 90))


def test_multi_client_tcp_stress(website):
    """Multiple clients TCP stress test with concurrent connections"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            
            # Create multiple clients concurrently
            async def client_stress_test(client_id):
                client_port = get_free_port()
                async with Client(token, ws_url=_ws_url(ws_port), socks_port=client_port) as cli:
                    # Each client makes multiple concurrent requests
                    tasks = []
                    for req_id in range(20):  # 20 requests per client
                        async def single_request(r_id):
                            await async_assert_web_connection(website, client_port)
                            return f"{client_id}_{r_id}"
                        tasks.append(single_request(req_id))
                    
                    results = await asyncio.gather(*tasks)
                    return client_id, results
            
            # Launch 10 clients concurrently
            start_time = time.time()
            client_tasks = []
            for i in range(10):
                client_tasks.append(client_stress_test(i))
            
            client_results = await asyncio.gather(*client_tasks)
            end_time = time.time()
            
            # Verify all clients completed successfully
            assert len(client_results) == 10, f"Expected 10 clients, got {len(client_results)}"
            
            # Verify each client completed all requests
            total_requests = 0
            for client_id, requests in client_results:
                assert len(requests) == 20, f"Client {client_id} missing requests: got {len(requests)}"
                total_requests += len(requests)
            
            assert total_requests == 200, f"Expected 200 total requests, got {total_requests}"
            
            # Verify timing
            duration = end_time - start_time
            assert duration < 60, f"Multi-client TCP stress test took too long: {duration}s"
    
    asyncio.run(asyncio.wait_for(_main(), 120))


def test_multi_client_udp_stress(udp_server):
    """Multiple clients UDP stress test with concurrent connections"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            
            # Create multiple clients concurrently
            async def client_udp_stress_test(client_id):
                client_port = get_free_port()
                async with Client(token, ws_url=_ws_url(ws_port), socks_port=client_port) as cli:
                    # Each client sends multiple UDP packets
                    tasks = []
                    for batch_id in range(10):  # 10 batches per client
                        async def udp_batch(b_id):
                            for i in range(5):  # 5 UDP packets per batch
                                await async_assert_udp_connection(udp_server, client_port)
                            return f"{client_id}_{b_id}"
                        tasks.append(udp_batch(batch_id))
                    
                    results = await asyncio.gather(*tasks)
                    return client_id, results
            
            # Launch 8 clients concurrently (8 * 10 * 5 = 400 UDP packets total)
            start_time = time.time()
            client_tasks = []
            for i in range(8):
                client_tasks.append(client_udp_stress_test(i))
            
            client_results = await asyncio.gather(*client_tasks)
            end_time = time.time()
            
            # Verify all clients completed successfully
            assert len(client_results) == 8, f"Expected 8 clients, got {len(client_results)}"
            
            # Verify each client completed all batches
            total_batches = 0
            for client_id, batches in client_results:
                assert len(batches) == 10, f"Client {client_id} missing batches: got {len(batches)}"
                total_batches += len(batches)
            
            assert total_batches == 80, f"Expected 80 total batches, got {total_batches}"
            
            # Verify timing
            duration = end_time - start_time
            assert duration < 90, f"Multi-client UDP stress test took too long: {duration}s"
    
    asyncio.run(asyncio.wait_for(_main(), 150))


def test_multi_client_mixed_stress(website, udp_server):
    """Mixed multiple clients stress test with both TCP and UDP traffic"""
    from wssocks import Server, Client
    
    async def _main():
        ws_port = get_free_port()
        
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            
            # TCP clients
            async def tcp_client_test(client_id):
                client_port = get_free_port()
                async with Client(token, ws_url=_ws_url(ws_port), socks_port=client_port) as cli:
                    tasks = []
                    for req_id in range(15):
                        async def tcp_request(r_id):
                            await async_assert_web_connection(website, client_port)
                            return f"tcp_{client_id}_{r_id}"
                        tasks.append(tcp_request(req_id))
                    results = await asyncio.gather(*tasks)
                    return f"tcp_{client_id}", results
            
            # UDP clients
            async def udp_client_test(client_id):
                client_port = get_free_port()
                async with Client(token, ws_url=_ws_url(ws_port), socks_port=client_port) as cli:
                    tasks = []
                    for batch_id in range(8):
                        async def udp_batch(b_id):
                            for i in range(5):
                                await async_assert_udp_connection(udp_server, client_port)
                            return f"udp_{client_id}_{b_id}"
                        tasks.append(udp_batch(batch_id))
                    results = await asyncio.gather(*tasks)
                    return f"udp_{client_id}", results
            
            # Launch mixed clients concurrently
            start_time = time.time()
            all_tasks = []
            
            # 5 TCP clients
            for i in range(5):
                all_tasks.append(tcp_client_test(i))
            
            # 5 UDP clients
            for i in range(5):
                all_tasks.append(udp_client_test(i))
            
            all_results = await asyncio.gather(*all_tasks)
            end_time = time.time()
            
            # Verify all clients completed
            assert len(all_results) == 10, f"Expected 10 mixed clients, got {len(all_results)}"
            
            # Separate and verify TCP and UDP results
            tcp_results = [r for r in all_results if r[0].startswith('tcp_')]
            udp_results = [r for r in all_results if r[0].startswith('udp_')]
            
            assert len(tcp_results) == 5, f"Expected 5 TCP clients, got {len(tcp_results)}"
            assert len(udp_results) == 5, f"Expected 5 UDP clients, got {len(udp_results)}"
            
            # Verify TCP clients completed all requests
            for client_name, requests in tcp_results:
                assert len(requests) == 15, f"TCP client {client_name} missing requests: got {len(requests)}"
            
            # Verify UDP clients completed all batches
            for client_name, batches in udp_results:
                assert len(batches) == 8, f"UDP client {client_name} missing batches: got {len(batches)}"
            
            # Verify timing
            duration = end_time - start_time
            assert duration < 120, f"Mixed multi-client stress test took too long: {duration}s"
    
    asyncio.run(asyncio.wait_for(_main(), 180))
