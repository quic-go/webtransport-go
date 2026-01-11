# Hello World Example

- A simple WebTransport example demonstrating bidirectional communication between client and server.
- if the client sends "hello", the server responds with "world!".

> [!NOTE]
> - Each message uses a **new stream**
> - Self-signed certificates used

## What This Example Shows

- Creating a WebTransport server
- Connecting a WebTransport client
- Opening bidirectional streams
- Sending and receiving messages

## How It Works

1. **Server** listens for WebTransport connections
2. **Client** connects and opens streams for each message
3. Server responds with "world!" for "hello", otherwise sends a default message


## Running the Example

### Terminal 1 - Start the Server
```bash
go run examples/helloWorld/server/main.go
```

### Terminal 2 - Start the Client
```bash
go run examples/helloWorld/client/main.go
```

### Try It Out
```
Enter message: hello
Server response: world!

Enter message: test
Server response: I only respond to 'hello' with 'world!'

Enter message: quit
```


### Server Side

```mermaid
flowchart TD
    A[Start Server] --> B[Listen on :4430]
    B --> C[Wait for Client]
    C --> D[Client Connected!]
    D --> E[Wait for Stream]
    E --> F[Read Message]
    F --> G{hello?}
    G -->|Yes| H[Send: world!]
    G -->|No| I[Send: default message]
    H --> E
    I --> E

    style A fill:#e1f5ff,color:#000
    style D fill:#e1ffe1,color:#000
    style G fill:#fff4e1,color:#000
```

**Key Functions:**
- `webtransport.Server{}` → Create server
- `server.Upgrade()` → Accept client connection
- `session.AcceptStream()` → Wait for incoming stream
- `stream.Write()` → Send response


### Client Side

```mermaid
flowchart TD
    A[Start Client] --> B[Connect to Server]
    B --> C[Connected!]
    C --> D[Get User Input]
    D --> E{quit?}
    E -->|Yes| F[Close & Exit]
    E -->|No| G[Open New Stream]
    G --> H[Send Message]
    H --> I[Read Response]
    I --> J[Show Response]
    J --> D

    style A fill:#e1f5ff,color:#000
    style C fill:#e1ffe1,color:#000
    style E fill:#fff4e1,color:#000
```

**Key Functions:**
- `webtransport.Dialer{}` → Create client
- `dialer.Dial()` → Connect to server
- `session.OpenStreamSync()` → Open new stream
- `stream.Write()` / `io.ReadAll()` → Send/receive data
- `session.CloseWithError()` → Graceful disconnect
