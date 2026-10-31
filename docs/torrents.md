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
/ts [-p provider] [-t type] <search_query>
/torsearch [-p provider] [-t type] <search_query>
/search [-p provider] [-t type] <search_query>
```

### Supported Flags
- **Category Filter (`-t` / `--type` or `-c` / `--category`)**:
  - `movie` — search movies (e.g. YTS, 1337x, Bitsearch, ThePirateBay, etc.)
  - `series` — search TV series (e.g. EZTV, 1337x, Bitsearch, etc.)
  - `anime` — search anime indexers (e.g. Nyaa, Tokyo Toshokan, SubsPlease, AnimeTosho, etc.)
  - `all` — (default) queries all providers matching their respective supported media types.
- **Provider Flag (`-p` / `--provider`)**:
  - Limit to specific providers, e.g. `-p nyaa`, `-p 1337x,yts,knaben`.
  - Providers include: `1337x`, `animesaturn`, `animetosho`, `bitsearch`, `bt4g`, `btdig`, `eztv`, `glotorrents`, `kickasstorrents`, `knaben`, `limetorrents`, `nekobt`, `nyaa`, `rutor`, `rutracker`, `subsplease`, `thepiratebay`, `therarbg`, `tokyotosho`, `torlock`, `torrentdownloads`, `torrentgalaxy`, `torznab`, `yts`.

### Examples
```text
/ts Avatar 2009                          # Search across all 24 enabled providers
/ts -t anime Frieren                     # Search anime providers
/ts -p yts Inception                     # Search YTS provider only
/ts -p thepiratebay,1337x Oppenheimer    # Search PirateBay and 1337x providers
/search -p nyaa Naruto Shippuden         # Search Nyaa anime provider only
```

### Key Search Engine Features
- **Magnetio JSON-RPC Architecture**: Searches across 24 independent torrent providers via an isolated Node.js Express JSON-RPC 2.0 sidecar (`magnetio-scraper`).
- **Comprehensive Scrape Coverage**: Queries may take up to ~25s in worst-case scenarios to allow slower providers (e.g. TorrentGalaxy, GloTorrents, TheRarBG) to respond, maximizing search completeness.
- **Inline Pagination & Telegraph Rendering**: View formatted HTML results directly in Telegram or full telegraph pages for large query results.
- **1-Click Magnet Share**: Each search result includes pre-constructed magnet links for instant sharing and downloading via `/tor`.
