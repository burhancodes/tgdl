# Magnetio Scraper JSON-RPC 2.0 API Specification

The Magnetio scraper sidecar exposes a **JSON-RPC 2.0** endpoint at `POST /rpc`.

---

## Authentication

If the `RPC_SHARED_SECRET` environment variable is configured on the scraper service, all incoming JSON-RPC calls must include a valid secret:
- **HTTP Header**: `Authorization: Bearer <RPC_SHARED_SECRET>`
- **OR Request Parameter**: `"secret": "<RPC_SHARED_SECRET>"` inside the JSON-RPC `params` object.

If authorization fails or is missing when `RPC_SHARED_SECRET` is set, the endpoint returns a JSON-RPC error with code `-32001` ("Unauthorized"). If `RPC_SHARED_SECRET` is unset, authentication is disabled.

---

## JSON-RPC 2.0 Methods

### 1. `torrent.search`

Scrapes enabled torrent providers in parallel and returns a deduplicated list of torrent results with pre-built magnet URIs.

#### Parameters

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | `string` | **Yes** | — | Free-text search terms |
| `type` | `string` | No | `"movie"` | Content type filter: `"movie"`, `"series"`, or `"anime"` |
| `year` | `number` | No | `null` | Release year filter |
| `season` | `number` | No | `null` | Season number (for series) |
| `episode` | `number` | No | `null` | Episode number (for series) |
| `providers` | `string[]` | No | `null` | Whitelist of provider IDs (e.g. `["thepiratebay", "yts"]`). Scrapes all 22 providers if omitted |
| `limit` | `number` | No | `null` | Maximum number of results to return |
| `strict` | `boolean` | No | `false` | Default `false` in JSON-RPC (allows broad free-text phrase matching). Set to `true` to enforce strict title phrase matching (default in REST `/streams` route) |

#### Example Request

```json
{
  "jsonrpc": "2.0",
  "method": "torrent.search",
  "params": {
    "query": "Ubuntu 24.04",
    "limit": 10,
    "strict": false
  },
  "id": 1
}
```

#### Example Response

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "count": 1,
    "torrents": [
      {
        "title": "ubuntu-24.04-desktop-amd64.iso",
        "infoHash": "45a305e26090e543666b6cfa45d6541f486431bd",
        "seeders": 150,
        "leechers": 10,
        "size": 4970422272,
        "provider": "ThePirateBay",
        "quality": null,
        "codec": null,
        "source": null,
        "languages": [],
        "magnet": "magnet:?xt=urn:btih:45a305e26090e543666b6cfa45d6541f486431bd&dn=ubuntu-24.04-desktop-amd64.iso&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce"
      }
    ]
  }
}
```

---

### 2. `torrent.providers`

Returns the list of all 22 available provider scrapers (id and display name).

#### Example Request

```json
{
  "jsonrpc": "2.0",
  "method": "torrent.providers",
  "id": 2
}
```

#### Example Response

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "providers": [
      { "id": "thepiratebay", "name": "ThePirateBay" },
      { "id": "yts", "name": "YTS" },
      { "id": "leetx", "name": "1337x" },
      { "id": "torrentgalaxy", "name": "TorrentGalaxy" },
      { "id": "nyaa", "name": "Nyaa" },
      { "id": "limetorrents", "name": "LimeTorrents" },
      { "id": "bitsearch", "name": "Bitsearch" }
    ]
  }
}
```

---

### 3. `torrent.health`

Returns service liveness and version status.

#### Example Request

```json
{
  "jsonrpc": "2.0",
  "method": "torrent.health",
  "id": 3
}
```

#### Example Response

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "status": "ok",
    "service": "magnetio-scraper",
    "version": "1.1.5"
  }
}
```

---

## Batch Requests

Standard JSON-RPC 2.0 batch array requests are supported. The server returns an array of response objects:

```json
[
  { "jsonrpc": "2.0", "method": "torrent.health", "id": 1 },
  { "jsonrpc": "2.0", "method": "torrent.providers", "id": 2 }
]
```

---

## Error Handling & Error Codes

The RPC service returns standard JSON-RPC 2.0 error objects `{ code, message }` on failure:

| Error Code | Message | Cause |
|---|---|---|
| `-32700` | Parse error | Invalid JSON body payload |
| `-32600` | Invalid Request | Missing or invalid `jsonrpc: "2.0"` framing |
| `-32601` | Method not found | Unknown method string |
| `-32602` | Invalid params | Missing required `query` parameter |
| `-32603` | Internal error | Unhandled runtime exception in scraper |
| `-32001` | Unauthorized | Mismatched or missing secret token when `RPC_SHARED_SECRET` is set |

---

## Environment Variables & Concurrency Configuration

The scraper sidecar uses the following environment variables:

| Environment Variable | Default Value | Description |
|---|---|---|
| `PORT` | `8080` | HTTP server listening port |
| `RPC_SHARED_SECRET` | `null` | Shared secret token for `/rpc` authorization |
| `CACHE_MAX_BYTES` | `20971520` (20MB) | Max in-memory cache size limit (evicts oldest entries via LRU) |
| `CACHE_TTL_STREAMS` | `3600` | Stream result cache TTL in seconds (1 hour) |
| `SCRAPER_CONCURRENCY` | `12` | Max concurrent provider scrapers (`p-limit`) |
| `SCRAPER_PROVIDER_TIMEOUT_MS` | `20000` (20s) | Per-provider scrape timeout |
| `SCRAPER_HARD_TIMEOUT_MS` | `30000` (30s) | Hard cutoff deadline for all providers in `scrapeAll()` |

---

## Curl Test Example

```bash
curl -X POST http://localhost:8080/rpc \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your_rpc_secret_here" \
  -d '{
    "jsonrpc": "2.0",
    "method": "torrent.search",
    "params": { "query": "Ubuntu", "limit": 5 },
    "id": 1
  }'
```
