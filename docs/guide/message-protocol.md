# Message Protocol

LinkSocks uses a custom binary message protocol over WebSocket. This page details the message transmission mechanisms and protocol specifications.

### Protocol Version

Current protocol version: `0x01`

## Message Types

### Authentication Messages

**AuthMessage**: Initial authentication from client to server
```
Version(1) + Type(1) + TokenLen(1) + Token(N) + Reverse(1) + Instance(16)
```

**AuthResponseMessage**: Server response to authentication
```
Version(1) + Type(1) + Success(1) + [ErrorLen(1) + Error(N) if !Success]
```

### Connection Management

**ConnectMessage**: Request to establish a new connection
```
Version(1) + Type(1) + Protocol(1) + ChannelID(16) + [AddrLen(1) + Addr(N) + Port(2) if TCP]
```

**ConnectResponseMessage**: Response to connection request
```
Version(1) + Type(1) + Success(1) + ChannelID(16) + [ErrorLen(1) + Error(N) if !Success]
```

**DisconnectMessage**: Request to close a connection
```
Version(1) + Type(1) + ChannelID(16) + [ErrorLen(1) + Error(N) if error]
```

### Data Transfer

**DataMessage**: Data transmission between client and server
```
Version(1) + Type(1) + Protocol(1) + ChannelID(16) + Compression(1) + DataLen(4) + Data(N) +
    [AddrLen(1) + Addr(N) + Port(2) if UDP]
```

## Channel Management

### Channel Identification

Each proxy connection is assigned a unique `ChannelID` (UUID) that serves multiple purposes:

- Links related messages across the WebSocket connection
- Enables multiplexing multiple proxy connections over a single WebSocket
- Facilitates message routing in agent proxy mode

### Message Queuing

LinkSocks uses channel-specific message queues to handle asynchronous message delivery. Each channel gets its own message queue with a buffer size of 1000 messages. When the queue is full, new messages are dropped to prevent memory exhaustion.

### Channel Cleanup

When connections close, the system automatically:
- Removes the channel from active channel maps
- Closes and removes the associated message queue
- Cleans up any pending goroutines

## TCP Connection Flow

### Forward Proxy Mode

1. **Connection Request**: Client sends ConnectMessage with target address and port
2. **Server Processing**: Server attempts to establish connection to target
3. **Response**: Server sends ConnectResponseMessage indicating success or failure
4. **Data Exchange**: Bidirectional DataMessage exchange until connection closes
5. **Cleanup**: DisconnectMessage sent when either side closes

### Reverse Proxy Mode

In reverse proxy mode, the server initiates connections to the client:

1. **Server Request**: Server sends ConnectMessage with target address and port
2. **Client Processing**: Client attempts to establish connection to target
3. **Response**: Client sends ConnectResponseMessage indicating success or failure
4. **Data Exchange**: Bidirectional DataMessage exchange until connection closes
5. **Cleanup**: DisconnectMessage sent when either side closes

## UDP Handling

UDP over SOCKS5 requires special handling due to its connectionless nature. The process involves SOCKS5 UDP association and WebSocket message exchange.

### SOCKS5 UDP Association Process

When a SOCKS5 client requests UDP ASSOCIATE (cmd=0x03):

1. **Local UDP Listener Creation**
   - LinkSocks creates a local UDP listener on an available port
   - This listener will receive UDP packets from the SOCKS5 client
   - The listener address is returned to the SOCKS5 client

2. **WebSocket Connection Setup**
   - LinkSocks sends a ConnectMessage to the server with Protocol="udp"
   - The ChannelID identifies this UDP association
   - No target address is included in the initial ConnectMessage for UDP

3. **SOCKS5 Response**
   - LinkSocks responds to the SOCKS5 client with the local UDP listener address
   - The client will send UDP packets to this address for forwarding

### UDP Data Processing Flow

#### Client → Server Direction

1. **Packet Reception**
   - Local UDP listener receives packet from SOCKS5 client
   - Packet includes SOCKS5 UDP header with target address information

2. **SOCKS5 UDP Header Parsing**
   - Extract target address and port from SOCKS5 UDP header format:
     ```
     RSV(2) + FRAG(1) + ATYP(1) + DST.ADDR(var) + DST.PORT(2) + DATA(var)
     ```

3. **WebSocket Transmission**
   - Create DataMessage with extracted target address
   - Send via WebSocket to server with Protocol="udp"

#### Server → Client Direction

1. **Server Reception**
   - Server receives UDP packet from remote target
   - Server knows the source address of the incoming packet

2. **SOCKS5 UDP Header Construction**
   - Server constructs SOCKS5 UDP header with source address information
   - Format matches SOCKS5 specification

3. **WebSocket Transmission**
   - Create DataMessage with SOCKS5 UDP header + data
   - Send to client via WebSocket

4. **Client Forwarding**
   - Client receives DataMessage and forwards to local UDP listener
   - SOCKS5 client receives packet with proper source address information

### UDP Connection Lifecycle

- **Association**: Lasts until SOCKS5 TCP control connection closes
- **Timeout**: UDP associations may timeout if inactive
- **Cleanup**: When association ends, local UDP listener is closed and resources freed

## Performance

### Data Compression

DataMessage supports conditional compression when data size exceeds the compression threshold (default: 512KB, configurable via `WithCompressionThreshold`). Compression flag in DataMessage is set to 0x01 for gzip compression.

### Batch Processing

LinkSocks implements dynamic batching for network data reads:

**SendManager Batching**
- Network data is batched in a buffer before creating DataMessage
- Default batch wait time: 20ms minimum, 500ms maximum
- Adaptive batching adjusts delay based on connection speed
- Timer-based flushing when delay expires or buffer is full

**Batching Control**
- Dynamic batching can be disabled via `WithDynamicBatching(false)` for immediate sending
- High-speed connections increase batch delay
- Low-speed connections reduce batch delay
- Buffer size and timing limits are configurable via `WithBatchingTimeLimits`

## Error Handling

### Client-Server Connection Failures

**When token validation fails**
- Server sends `AuthResponseMessage{Success: false, Error: "invalid token"}`
- WebSocket connection is immediately closed
- Client receives nonRetriableError and stops reconnection attempts

**When WebSocket connection is lost**
- Client checks if reconnect is enabled and error is not nonRetriableError
- Waits ReconnectDelay (default: 5s; configurable via ClientOption.WithReconnectDelay) before attempting reconnection
- Existing channels remain in memory, DataMessages continue queuing
- Data transmission resumes after successful reconnection

**When no clients are available for load balancing**
- Server waits 10 seconds to find available reverse proxy clients
- Uses round-robin selection with ping liveness checks
- If ping fails, selects next available client
- After 10 seconds with no clients, sends error code 0x03 to SOCKS5 client

### Network Connection Failures

**When target server connection fails**
- Normal mode: immediately sends `ConnectResponseMessage{Success: false, Error: details}`
- SOCKS5 client receives error response (code 0x04) and connection is closed
- FastOpen mode: assumes success initially, detects failure on first data transmission
- FastOpen timeout: disconnects channel if no success confirmation after ConnectTimeout+5 seconds

**When remote peer closes connection**
- Sends `DisconnectMessage{ChannelID: channelID, Error: details}`
- Cleans up resources: removes message queue, deletes from channel maps, cancels context
- Closes SOCKS5 client connection to signal termination

**When connection closes abnormally**
- Channel cleaner checks activity every 60 seconds
- Channels inactive for 12 hours are automatically cleaned up
- RST packets or network timeouts trigger immediate resource cleanup
- Individual channel failures do not affect other channels on the same WebSocket