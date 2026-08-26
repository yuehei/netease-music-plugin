# Navidrome 网易云音乐元数据插件

> 本插件基于 Navidrome 官方的 [apple-music-plugin](https://github.com/navidrome/apple-music-plugin) 改造而来——继承了其成熟的整体机制（艺术家 ID 解析、多级缓存与负缓存、专辑名三段式匹配、能力开关等），并将数据源整体替换为 [NeteaseCloudMusicApi](https://github.com/Binaryify/NeteaseCloudMusicApi) 兼容的 HTTP API：原本依赖 HTML 刮削的简介、图片、相似艺术家等能力，全部改为直接调用结构化 JSON 接口，更稳定也更快速。

**注意：本插件需要 Navidrome 0.61.0 或更高版本。**

This plugin fetches artist and album metadata from Netease Cloud Music (网易云音乐) through a [NeteaseCloudMusicApi](https://github.com/Binaryify/NeteaseCloudMusicApi)-compatible HTTP API — no API key required for most endpoints.
It provides artist biographies, images, similar artists, top songs, album artwork, and album editorial notes.

## Features

- Fetches artist biographies via `/artist/detail`
- Retrieves artist images in multiple sizes (1500x1500, 600x600, 300x300) via Netease image resizing (`?param=WxH`)
- Retrieves album artwork in multiple sizes
- Fetches album editorial notes and Netease album URLs
- Discovers similar artists via `/simi/artist` (requires login cookie)
- Fetches artist hot songs via `/artist/top/song`
- Provides Netease Cloud Music artist page URLs
- Multiple API endpoints: configure several mirrors — tried in random order per request with automatic failover when a mirror is down or rate-limited
- Aggressive caching with negative caching (2-hour TTL for "not found" results) to minimize external requests

## Installation

1. 从 [Releases 页面](https://github.com/yuehei/netease-music-plugin/releases/)下载最新的 `netease-music.ndp`（或自行构建）
2. Copy it to your Navidrome plugins folder. Default location: `<navidrome-data-directory>/plugins/`
3. Add `netease-music` to the `Agents` [configuration option](https://www.navidrome.org/docs/usage/configuration/options/#advanced-configuration). For example:
   ```toml
   # navidrome.toml
   Agents = "netease-music,deezer,lastfm"
   ```
   Or using an environment variable:
   ```
   ND_AGENTS=netease-music,deezer,lastfm
   ```
   The order determines priority — agents are tried in the specified order until one succeeds.
4. Open Navidrome and go to **Settings > Plugins > 网易云音乐元数据代理**
5. Configure the plugin (see [Configuration](#configuration) below)
6. Toggle the plugin to **Enabled**

## Configuration

Access the plugin configuration in Navidrome: **Settings > Plugins > 网易云音乐元数据代理**

### Configuration Fields

#### API 地址
- **默认值**：*（空，安装后必须自行填写）*
- **说明**：NeteaseCloudMusicApi 兼容服务的 Base URL，每行填写一个
- **工作方式**：每次请求随机从中选取一个地址，多个镜像分摊请求量
- **示例**：
  ```
  https://api.example.com
  http://localhost:3000/
  ```
- **自部署**：可以从 [NeteaseCloudMusicApi](https://github.com/Binaryify/NeteaseCloudMusicApi) 项目（或其增强分支）部署自己的实例，并把地址填到这里
- **注意**：未配置时插件所有能力都会报错 `api_endpoints not configured`，填写后恢复

#### MUSIC_U Cookie
- **默认值**：*（空）*
- **说明**：网易云音乐登录态 Cookie 中 `MUSIC_U` 的值
- **用途**：部分接口需要登录——主要是 `/simi/artist`（相似艺术家）。未配置时这些接口返回 `301`，插件优雅降级（对应能力返回空数据）
- **获取方式**：在浏览器登录网易云音乐网页版，打开开发者工具（Application > Cookies）复制 `MUSIC_U` 的值
- **安全提示**：该值会以 `cookie` 查询参数发送给所配置的 API 地址。请只配置你信任的地址，也不要把个人 cookie 打进对外分发的插件包

#### 缓存时间（天）
- **默认值**：`7`
- **说明**：拉取到的元数据（简介、图片、相似艺术家等）缓存多少天后重新获取
- **注意**：艺术家 ID 映射永久缓存（不会变化）；"未找到"的结果（无匹配的艺术家或专辑）缓存 2 小时，避免重复请求

#### Capabilities
- **Default**: All enabled except Album Images
- **What it is**: Each capability (Artist URL, Artist Biography, Artist Images, Similar Artists, Top Songs, Album Images, Album Info) can be individually toggled on or off. When disabled, the plugin will skip that capability and Navidrome will fall through to the next configured agent.

## How It Works

### Plugin Capabilities

The plugin implements seven metadata provider capabilities:

| Capability             | Purpose                                                              |
|------------------------|----------------------------------------------------------------------|
| **GetArtistURL**       | Returns the Netease Cloud Music artist page URL                      |
| **GetArtistBiography** | Fetches artist biography via `/artist/detail`                        |
| **GetArtistImages**    | Retrieves artist images in three sizes                               |
| **GetSimilarArtists**  | Discovers similar artists via `/simi/artist` (login cookie required) |
| **GetArtistTopSongs**  | Fetches hot songs via `/artist/top/song`                             |
| **GetAlbumImages**     | Retrieves album artwork in three sizes via `/artist/album`           |
| **GetAlbumInfo**       | Returns album editorial notes and the Netease album URL              |

### Host Services

| Service     | Usage                                                                       |
|-------------|-----------------------------------------------------------------------------|
| **HTTP**    | NeteaseCloudMusicApi calls against the configured endpoints                 |
| **KVStore** | Cache artist ID mappings and fetched metadata to reduce external requests   |
| **Config**  | User-configurable API endpoints, MUSIC_U cookie, and cache TTL              |
| **Logging** | Debug and error logging for troubleshooting                                 |

### Flow

1. **Artist lookup** — Searches `/search?type=100` by artist name and caches the Netease artist ID
2. **Artist detail** — Fetches `/artist/detail?id=…` for biography and images (one call serves both capabilities)
3. **Similar artists** — Fetches `/simi/artist?id=…` with the configured cookie; degrades gracefully on `301`
4. **Album lookup** — Resolves the artist ID, lists the artist's albums via `/artist/album?id=…&limit=200`, then matches the album name locally
5. **Endpoint selection** — Endpoints are tried in random order per request; when a mirror is unreachable (transport error, non-200 status) or rate-limited (API code 405/429/460/462) the next mirror is tried automatically, and an error is returned only when every mirror fails
6. **Caching** — Stores results in KVStore with configurable TTL; caches genuine "not found" results (API code 200 with empty results) with a 2-hour TTL to avoid repeated lookups. Rate limiting and other API-level failures are NOT cached, so the next scan retries them

### Data Sources

| Source                       | Path                                    | Data                                   |
|------------------------------|-----------------------------------------|----------------------------------------|
| `{endpoint}/search`          | `?keywords=…&type=100`                  | Artist ID resolution                   |
| `{endpoint}/artist/detail`   | `?id=…`                                 | Biography, images                      |
| `{endpoint}/simi/artist`     | `?id=…` (login required)                | Similar artists                        |
| `{endpoint}/artist/top/song` | `?id=…`                                 | Top songs                              |
| `{endpoint}/artist/album`    | `?id=…&limit=200`                       | Album artwork, album ID                |
| `{endpoint}/album`           | `?id=…`                                 | Album editorial notes                  |

### Files

| File                           | Description                                       |
|--------------------------------|---------------------------------------------------|
| [main.go](main.go)             | Plugin implementation — all metadata capabilities |
| [manifest.json](manifest.json) | Plugin metadata and permission declarations       |
| [Makefile](Makefile)           | Build automation                                  |

## Building

### Prerequisites
- **Recommended**: [TinyGo](https://tinygo.org/getting-started/install/) (produces smaller binary size)
- **Alternative**: Standard Go 1.25+ (larger binary but easier setup)

### Quick Build (Using Makefile)
```sh
# Run tests
make test

# Build plugin.wasm
make build

# Create distributable plugin package
make package
```

The `make package` command creates `netease-music.ndp` containing the compiled WebAssembly module and manifest.

### Manual Build Options

#### Using TinyGo (Recommended)
```sh
# Install TinyGo first: https://tinygo.org/getting-started/install/
tinygo build -opt=2 -scheduler=none -no-debug -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip netease-music.ndp plugin.wasm manifest.json
```

#### Using Standard Go
```sh
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
zip netease-music.ndp plugin.wasm manifest.json
```

### Output
- `plugin.wasm`: The compiled WebAssembly module
- `netease-music.ndp`: The complete plugin package ready for installation
