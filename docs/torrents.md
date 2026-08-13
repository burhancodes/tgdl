# Torrent & Magnet Link Downloader & Search Engine

This guide covers torrent and magnet downloading via `aria2c` and the interactive Torrent Search Engine.

---

## Torrent Downloader (`/tor`)

Download torrent files or magnet links headlessly with real-time peer count, seeders, and speed metrics.

### Syntax
```text
/tor <magnet_link_or_url>
```
Or reply to a `.torrent` file attachment in chat with `/tor`.

### Examples
- Download a magnet link:
  ```text
  /tor magnet:?xt=urn:btih:123456789abcdef...
  ```
- Download a `.torrent` URL:
  ```text
  /tor https://example.com/file.torrent
  ```
- Uploading `.torrent` file:
  Send a `.torrent` file to the chat and reply to it with `/tor`.

---

## Torrent Search Engine (`/ts`, `/torsearch`, `/search`)

Search for torrents across multiple providers directly within Telegram.

### Syntax
```text
/ts [provider_flags] <search_query>
/torsearch [provider_flags] <search_query>
/search [provider_flags] <search_query>
```

### Supported Provider Flags
Specify one or more provider flags to limit search to target indexers:
- **Movie / Media Indexers**: `-yts`, `-tpb` / `-piratebay`, `-1337x` / `-leetx`, `-tgx` / `-torrentgalaxy`, `-kat` / `-kickass`, `-lime`, `-rarbg`, `-glo`
- **Anime Indexers**: `-nyaa`, `-subsplease`, `-tosho` / `-animetosho`, `-neko`
- **General / Russian Indexers**: `-bitsearch`, `-bt4g`, `-btdig`, `-torlock`, `-td`, `-eztv`, `-rutor`, `-rutracker`, `-torznab`
- **Generic syntax**: `-p=<provider_id>` (e.g. `-p=yts`, `-p=thepiratebay`)

### Examples
```text
/ts Avatar 2009                          # Search across all 22 enabled providers
/ts -yts Inception                       # Search YTS provider only
/ts -tpb -1337x Oppenheimer              # Search PirateBay and 1337x providers
/search -nyaa Naruto Shippuden           # Search Nyaa anime provider only
/torsearch -p=torrentgalaxy Dune Part 2  # Search TorrentGalaxy using generic flag
```

### Key Search Engine Features
- **Magnetio JSON-RPC Architecture**: Searches across 22 independent torrent providers (ThePirateBay, 1337x, YTS, Kickass, Nyaa, LimeTorrents, Bitsearch, BT4G, BTdig, etc.) via an isolated Node.js Express JSON-RPC 2.0 sidecar (`magnetio-scraper`).
- **Comprehensive Scrape Coverage**: Queries may take up to ~25s in worst-case scenarios to allow slower providers (e.g. TorrentGalaxy, GloTorrents, TheRarBG) to respond, maximizing search completeness.
- **Inline Pagination & Telegraph Rendering**: View formatted HTML results directly in Telegram or full telegraph pages for large query results.
- **1-Click Magnet Share**: Each search result includes pre-constructed magnet links for instant sharing and downloading via `/tor`.
