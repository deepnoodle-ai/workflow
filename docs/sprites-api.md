# Sprites API Documentation

The Sprites API is a service for provisioning and managing isolated Linux sandboxes with persistent filesystems.

**Base URL:** `https://api.sprites.dev/v1`

## Authentication

All requests require a bearer token via the `Authorization` header:

```
Authorization: Bearer $SPRITES_TOKEN
```

## SDKs

- **Go:** `github.com/superfly/sprites-go`
- **Node.js:** `@fly/sprites`
- **Python:** `sprites-py`
- **Elixir:** `superfly/sprites-ex`

---

## Sprites

Sprites are persistent environments that hibernate when idle and automatically wake on demand.

### Create Sprite

**POST** `/v1/sprites`

Creates a new sprite with a unique organization name.

**Request Body:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Unique sprite identifier |
| `url_settings.auth` | string | No | `"sprite"` or `"public"` (default: `"sprite"`) |

**Response:** `201 Created`

```json
{
  "id": "string",
  "name": "string",
  "organization": "string",
  "url": "string",
  "status": "string",
  "created_at": "timestamp",
  "updated_at": "timestamp",
  "url_settings": {
    "auth": "sprite"
  }
}
```

**Error Codes:** `400` Invalid request, `401` Unauthorized

### List Sprites

**GET** `/v1/sprites`

Lists all sprites for the authenticated organization.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `prefix` | string | Filter by name prefix |
| `max_results` | int | 1-50 results (default: 50) |
| `continuation_token` | string | Pagination token |

**Response:** `200 Success`

```json
[
  {
    "id": "string",
    "name": "string",
    "organization": "string",
    "url": "string",
    "status": "string",
    "created_at": "timestamp",
    "updated_at": "timestamp"
  }
]
```

### Get Sprite

**GET** `/v1/sprites/{name}`

Retrieves details for a specific sprite.

**Response:** `200 Success` - Returns sprite object

**Error Codes:** `401` Unauthorized, `404` Not found

### Update Sprite

**PUT** `/v1/sprites/{name}`

Modifies sprite settings.

**Request Body:**

```json
{
  "url_settings": {
    "auth": "sprite" | "public"
  }
}
```

**Response:** `200 Success` - Returns updated sprite object

**Error Codes:** `400` Invalid, `401` Unauthorized, `404` Not found

### Delete Sprite

**DELETE** `/v1/sprites/{name}`

Permanently deletes a sprite and all associated resources.

**Response:** `204 No Content`

**Error Codes:** `401` Unauthorized, `404` Not found

---

## Command Execution (Exec)

Execute and manage processes within sprites.

### Execute Command (WebSocket)

**WSS** `/v1/sprites/{name}/exec`

Executes commands via persistent WebSocket connections that survive disconnections.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `cmd` | string | Command and arguments (repeatable) |
| `tty` | bool | Enable TTY mode for interactive sessions |
| `stdin` | bool | Enable standard input |
| `cols` | int | Terminal columns |
| `rows` | int | Terminal rows |
| `max_run_after_disconnect` | duration | Persistence timeout after disconnect |
| `env` | string | Environment variables as `KEY=VALUE` (repeatable) |

**Binary Protocol (non-PTY mode):**

Frames use a 1-byte stream identifier prefix:

| Byte | Direction | Stream |
|------|-----------|--------|
| `0` | client -> server | stdin |
| `1` | server -> client | stdout |
| `2` | server -> client | stderr |
| `3` | server -> client | exit code |
| `4` | client -> server | stdin EOF |

**JSON Messages:**

Resize terminal:
```json
{"type": "resize", "cols": 80, "rows": 24}
```

Session info (returned by server):
```json
{
  "type": "session_info",
  "pid": 1234,
  "command": ["bash"],
  "tty": true
}
```

Exit message:
```json
{"type": "exit", "code": 0}
```

Port notification (server sends when ports open/close):
```json
{"type": "port_notification", "port": 8080, "open": true}
```

**Response:** `101 Switching Protocols`

### Execute Command (HTTP)

**POST** `/v1/sprites/{name}/exec`

Simple HTTP alternative for non-TTY command execution. Parameters match WebSocket version.

**Response:** `200 Success`

### List Exec Sessions

**GET** `/v1/sprites/{name}/exec`

Returns all active execution sessions.

**Response:** `200 Success`

```json
[
  {
    "id": 123,
    "command": "bash",
    "created": "timestamp",
    "is_active": true,
    "tty": true,
    "bytes_per_second": 1024.5,
    "workdir": "/home/user"
  }
]
```

### Attach to Session

**WSS** `/v1/sprites/{name}/exec/{session_id}`

Reconnect to an existing session and retrieve scrollback buffer containing output that occurred while disconnected.

**Response:** `101 Switching Protocols`

### Kill Session

**POST** `/v1/sprites/{name}/exec/{session_id}/kill`

Terminates a session with streaming NDJSON response.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `signal` | string | `SIGTERM` | Signal type |
| `timeout` | duration | `10s` | Kill timeout |

**Response Events (NDJSON):**

- `signal` - Signal sent
- `timeout` - Timeout occurred
- `exited` - Process exited
- `killed` - Process killed
- `error` - Error occurred
- `complete` - Operation complete

---

## Checkpoints

Create and manage filesystem snapshots for instant rollback.

### Create Checkpoint

**POST** `/v1/sprites/{name}/checkpoint`

Creates a new checkpoint snapshot with streaming progress updates.

**Request Body:**

```json
{
  "comment": "optional description"
}
```

**Response:** `200 Success` - Streaming NDJSON

```json
{"type": "info", "data": "Creating checkpoint...", "time": "timestamp"}
{"type": "complete", "data": "v7", "time": "timestamp"}
```

**Event Types:**
- `info` - Progress information
- `error` - Error occurred
- `complete` - Checkpoint created (data contains checkpoint ID)

### List Checkpoints

**GET** `/v1/sprites/{name}/checkpoints`

Retrieves all checkpoints for a sprite.

**Response:** `200 Success`

```json
[
  {
    "id": "v7",
    "create_time": "2024-01-15T10:30:00Z",
    "source_id": "v6",
    "comment": "Before major changes"
  }
]
```

### Get Checkpoint

**GET** `/v1/sprites/{name}/checkpoints/{checkpoint_id}`

Retrieves details for a specific checkpoint.

**Response:** `200 Success` - Returns checkpoint object

### Restore Checkpoint

**POST** `/v1/sprites/{name}/checkpoints/{checkpoint_id}/restore`

Restores the sprite to a specified checkpoint state.

**Response:** `200 Success` - Streaming NDJSON (same format as Create)

---

## Network Policy

Control outbound network access with DNS-based filtering.

### Get Network Policy

**GET** `/v1/sprites/{name}/policy/network`

Retrieves the current network policy configuration.

**Response:** `200 Success`

```json
{
  "rules": [
    {
      "domain": "*.github.com",
      "action": "allow"
    },
    {
      "domain": "example.com",
      "action": "deny"
    }
  ]
}
```

### Set Network Policy

**POST** `/v1/sprites/{name}/policy/network`

Updates the network policy. Changes take immediate effect.

**Request Body:**

```json
{
  "rules": [
    {
      "domain": "*.github.com",
      "action": "allow"
    },
    {
      "domain": "*.npmjs.org",
      "action": "allow"
    },
    {
      "include": "preset-bundle-name"
    }
  ]
}
```

**Rule Properties:**

| Property | Type | Description |
|----------|------|-------------|
| `domain` | string | Domain pattern (supports `*` wildcard for subdomains) |
| `action` | string | `"allow"` or `"deny"` |
| `include` | string | Preset rule bundle name |

**Response:** `200 Success` - Returns updated policy

**Error Codes:** `400` Bad Request, `404` Not Found, `500` Server Error

---

## Port Proxy

Tunnel TCP connections to services running inside a sprite.

### Proxy Connection

**WSS** `/v1/sprites/{name}/proxy`

After the WebSocket handshake, the connection becomes a transparent TCP relay.

**Initial Handshake:**

Client sends:
```json
{"host": "localhost", "port": 8080}
```

Server responds:
```json
{"status": "connected", "target": "localhost:8080"}
```

**Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `host` | string | Target hostname (typically `"localhost"`) |
| `port` | int | Target port (1-65535) |

**Data Transmission:**

After the JSON handshake, the connection operates as a raw TCP relay:

| Direction | Description |
|-----------|-------------|
| Client -> Server | Raw bytes sent to target TCP port |
| Server -> Client | Raw bytes received from target TCP port |

**Use Cases:**
- Accessing development servers
- Connecting to databases
- Any TCP service running in the sprite

**Response:** `101 Switching Protocols`

**Error Codes:** `400` Bad Request, `404` Not Found

---

## Common Response Codes

| Code | Description |
|------|-------------|
| `101` | WebSocket upgrade successful |
| `200` | Success |
| `201` | Created |
| `204` | No Content |
| `400` | Bad Request - Invalid parameters |
| `401` | Unauthorized - Invalid or missing token |
| `404` | Not Found - Resource doesn't exist |
| `500` | Server Error |
