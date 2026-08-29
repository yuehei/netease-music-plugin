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
- Fetches LRC lyrics (with Chinese translation when available) via `/lyric/new`, falling back to `/lyric`
- Provides Netease Cloud Music artist page URLs
- Multiple API endpoints: configure several mirrors — random order per request with one automatic failover when a mirror is down or rate-limited
- **艺术家全字匹配开关**：开启后，搜索不到与艺术家名称完全一致的结果时不再猜测，而是跳过交给下一个 agent（如 last.fm / ListenBrainz）刮削——两个 agent 配合使用匹配更准，也避免缓存错误的艺术家 ID
- **艺术家 ID 映射文件**：在音乐库中维护一个 JSON 文件，手工指定"艺术家名（支持 `;` 分隔的多个别名）→ 网易云 ID"，保存插件配置或修改文件后自动加载进内存，优先级高于自动搜索
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
- **说明**：拉取到的元数据（简介、图片、相似艺术家等）缓存多少天后重新获取；歌词相关缓存（歌曲 ID、歌词）单独由"歌词缓存时间（天）"控制
- **注意**：艺术家 ID 映射永久缓存（不会变化）；"未找到"的结果（无匹配的艺术家或专辑）缓存 2 小时，避免重复请求

#### 艺术家全字匹配
- **默认值**：关闭
- **说明**：开启后，`/search?type=100` 的结果中没有与艺术家名称完全一致（忽略大小写）的条目时，不再回退到第一个候选，而是本插件直接跳过（返回"无结果"）
- **配合使用**：Navidrome 会继续调用 Agents 列表中的下一个 agent（如 `lastfm`、`listenbrainz`），由它们完成这位艺术家的刮削——网易云只提供能精确匹配的 ID，其余交给更擅长的 agent，整体匹配更准，且不会把错误的 ID 永久写进缓存
- **建议**：已映射到错误 ID 的艺术家，可配合下面的"艺术家 ID 映射文件"手工纠正

#### 艺术家 ID 映射文件
- **默认值**：*（空）*
- **说明**：音乐库内一个 JSON 文件的**相对路径**（如 `meta/artist-ids.json`），固定从**默认音乐库**（library id = 1）读取。手工维护"艺术家名 → 网易云 ID"映射。命中的艺术家直接使用配置的 ID 并同步更新缓存，优先级高于自动搜索匹配
- **文件格式**：键为艺术家名称，支持用英文分号 `;` 分隔多个别名（全部指向同一个 ID）；值为网易云艺术家 ID（数字或数字字符串），非法条目忽略。完整示例见 [example/artist-ids.json](example/artist-ids.json)
  ```json
  {
    "i-dle;G(idle);(G)I-DLE;G(I-DLE)": 14055085,
    "Miyeon;曺薇娟;조미연": 15249075
  }
  ```
- **名称匹配规则**：仅做**转小写 + 去首尾空白**两个归一化（`Miyeon` = `MIYEON` = ` miyeon `），**标点/括号/内部空格不归一化**（`G(idle)` ≠ `(G)I-DLE`）——库里标签是什么写法，就在别名里列出什么写法；全角半角、多艺术家组合（`A • B`）也不会拆分或转换
- **动态加载**：Navidrome 插件没有"配置已保存"回调，插件采用等价策略：**保存插件配置**（路径变化）立即重新加载；**直接在音乐库里编辑该文件**后，最迟 60 秒内检测到 mtime/大小变化并自动重载。由于 Navidrome 为**每次插件调用创建新的 wasm 实例**（实例内存不共享），解析结果通过宿主的 Cache 服务（进程内存、按插件隔离、全实例共享，需要 `cache` 权限）保存——60 秒节流跨所有实例生效，正常解析不再重复读文件。检测到变动重载时会写一条 Info 日志（含新旧条目数），便于确认生效
- **权限**：需要授予音乐库文件系统（只读）访问权限，并在启用插件时开启"允许所有媒体库"（Allow all libraries），否则无法读取该文件

#### Lyrics
- **默认值**：关闭
- **说明**：通过"歌手 + 歌名"搜索单曲（`/search?type=1`），再经 `/lyric/new`（失败回退 `/lyric`）抓取 LRC 歌词，并自动把网易云混在 LRC 里的 JSON 元信息行（作词/作曲等）转换成标准 LRC 时间标签
- **展示形态**：由"歌词显示方式"决定（见下），默认原文第一条、翻译 `lang=zh` 第二条
- **多端展示说明**：OpenSubsonic `getLyricsBySongId` 的 `structuredLyrics` 本身支持多条（每条独立 `lang`）；支持多歌词的客户端（Navidrome Web UI、Symfonium、Substreamer、Feishin 等）默认取第一条或提供语言切换。仅支持旧版 `/rest/getLyrics` 的老客户端拿不到插件歌词（Navidrome 服务端限制）
- **缓存**：歌曲 ID 与歌词均按"歌词缓存时间（天）"缓存；"未找到歌词"同样缓存，避免反复请求
- **记录曲目路径**：缓存歌词时会一并记录该曲目在音乐库内的相对路径（依赖 `library.filesystem` 权限，Navidrome 在请求中提供），供 [scripts/export_lyrics.py](scripts/export_lyrics.py) 写回
- **找不到歌词时**：插件返回"无歌词"，Navidrome 会尝试下一个歌词来源

#### 歌词显示方式
- **默认值**：`原文`
- **可选值**：
  - **原文**：原文与中文翻译各一条下发（翻译 `lang=zh`），由客户端自行决定显示哪一条
  - **原文+中文翻译**：按时间轴把翻译合并进原文，输出单条 LRC，每行为"原文（译文）"；时间戳对不齐的行保持原样，翻译独有的行丢弃
  - **中文**：仅输出中文翻译这一条
- **回退**：后两种方式在网易云没有提供该歌翻译时，自动回退为"原文"方式
- **切换**：缓存的仍是原文与翻译两部分，切换显示方式即时生效，无需等待缓存过期

#### 歌词缓存时间（天）
- **默认值**：`7`
- **说明**：歌词相关缓存（单曲搜索结果 `song:*` 与歌词 `lyrics:*`）多少天后重新获取，独立于通用"缓存时间（天）"；歌词基本不变，可按需调大以减少重复请求

#### 导出缓存歌词到音乐库（scripts/export_lyrics.py）

`cachedLyrics` 缓存位于 Navidrome 插件 KVStore（SQLite，每插件一个库）：
`<navidrome数据目录>/plugins/<插件名>/kvstore.db`。`scripts/export_lyrics.py` 读取其中所有歌词缓存，
按缓存里记录的曲目路径写回音乐库——生成同名 `.lrc` 文件或内嵌音频标签：

```sh
python3 scripts/export_lyrics.py \
  --db      /var/lib/navidrome/plugins/<插件名>/kvstore.db \  # KVStore 位置（必填）
  --library /music \                                          # 音乐库根目录（必填）
  [--mode lrc|tags|both]      # 写回方式：lrc=同名 .lrc（默认），tags=内嵌标签，both=两者
  [--include-translated]      # 中文翻译追加在原文后（空行隔开）
  [--dry-run] [--force]       # 只打印计划 / 覆盖已有
```

- `tags` 模式需要 [mutagen](https://mutagen.readthedocs.io/)（`pip install mutagen`）：mp3 → USLT、flac/ogg → LYRICS、m4a → ©lyr
- KVStore 为 SQLite WAL 模式，Navidrome 运行期间也可安全只读导出
- 注意：旧缓存条目（本功能上线前写入的）没有记录路径，会显示为"缺少路径"并跳过，待缓存过期重取后自动补上

#### Capabilities
- **Default**: All enabled except Album Images and Lyrics（Lyrics 为 opt-in，需显式开启）
- **What it is**: Each capability (Artist URL, Artist Biography, Artist Images, Similar Artists, Top Songs, Album Images, Album Info) can be individually toggled on or off. When disabled, the plugin will skip that capability and Navidrome will fall through to the next configured agent. Lyrics 单独一行，默认关闭。

### 安装注意事项（Library 权限与 Lyrics 生效条件）

1. **Library 权限（一次性授权）**：插件需要音乐库文件系统**只读**权限（读取艺术家 ID 映射文件、获取曲目路径）。首次启用时会要求授权音乐库：
   - **需要开启 "Allow all libraries"**，否则插件无法读取映射文件（实测仅勾选单个库时可能读取不到）
   - **不要**开 "Allow write access"（插件只读，从不写音乐库）
   - 授权一次后永久生效，重启不再提示
   - **更新插件包**（替换 .ndp 文件）后插件会被重置为禁用，需重新启用并重新授权
2. **歌词要生效**，必须把插件名加进 Navidrome 的 `LyricsPriority` 配置（歌词来源优先级），否则 Navidrome 不会调用任何歌词插件：
   ```toml
   # navidrome.toml
   LyricsPriority = "netease-music,.ttml,.yaml,.yml,.elrc,.lrc,.srt,.txt,embedded"
   ```
3. **配置修改需重启**：插件配置（如 API 地址）在插件实例启动时快照，改配置后需重启 Navidrome 才对新实例生效。

## How It Works

### Plugin Capabilities

The plugin implements seven metadata provider capabilities plus a lyrics capability:

| Capability             | Purpose                                                              |
|------------------------|----------------------------------------------------------------------|
| **GetArtistURL**       | Returns the Netease Cloud Music artist page URL                      |
| **GetArtistBiography** | Fetches artist biography via `/artist/detail`                        |
| **GetArtistImages**    | Retrieves artist images in three sizes                               |
| **GetSimilarArtists**  | Discovers similar artists via `/simi/artist` (login cookie required) |
| **GetArtistTopSongs**  | Fetches hot songs via `/artist/top/song`                             |
| **GetAlbumImages**     | Retrieves album artwork in three sizes via `/artist/album`           |
| **GetAlbumInfo**       | Returns album editorial notes and the Netease album URL              |
| **GetLyrics**          | Resolves the song by artist:title, fetches LRC lyrics (+ translation)|

### Host Services

| Service     | Usage                                                                       |
|-------------|-----------------------------------------------------------------------------|
| **HTTP**    | NeteaseCloudMusicApi calls against the configured endpoints                 |
| **KVStore** | Cache artist ID mappings and fetched metadata to reduce external requests   |
| **Cache**   | Share the parsed overrides file state across plugin instances (in-memory) |
| **Config**  | User-configurable API endpoints, MUSIC_U cookie, and cache TTL              |
| **Library** | Read-only access to the artist ID overrides file inside the music library   |
| **Logging** | Debug and error logging for troubleshooting                                 |

### Flow

1. **Artist lookup** — Searches `/search?type=100` by artist name and caches the Netease artist ID. The manual overrides file (if configured) takes precedence and refreshes the cache; with **exact-match-only** enabled a fuzzy miss skips the plugin so the next agent takes over. 并发的首次解析各自搜索一次（Navidrome 的 wasm 实例无法真实等待——宿主仅提供真实墙钟，单调时钟为虚拟，`time.Sleep` 不消耗真实时间），结果幂等写入 KVStore，后续调用全部命中缓存，重复上限为 Navidrome 的调用并发度（实测 ≤2）
2. **Artist detail** — Fetches `/artist/detail?id=…` for biography and images (one call serves both capabilities)
3. **Similar artists** — Fetches `/simi/artist?id=…` with the configured cookie; degrades gracefully on `301`
4. **Album lookup** — Resolves the artist ID, lists the artist's albums via `/artist/album?id=…&limit=200`, then matches the album name locally
5. **Lyrics** — Resolves the song via `/search?type=1` (title+artist), fetches `/lyric/new?id=…` (fallback `/lyric?id=…`), normalizes embedded JSON meta lines into standard LRC, and returns the translation as a second entry when available
6. **Endpoint selection** — Up to 2 endpoints are tried in random order per request (first pick + one failover); when a mirror is unreachable (transport error, non-200 status) or rate-limited (API code 405/429/460/462) one other mirror is tried automatically, and an error is returned when the attempt budget is exhausted
7. **Caching** — Stores results in KVStore with configurable TTL; caches genuine "not found" results (API code 200 with empty results) with a 2-hour TTL to avoid repeated lookups. Rate limiting and other API-level failures are NOT cached, so the next scan retries them

### Data Sources

| Source                       | Path                                    | Data                                   |
|------------------------------|-----------------------------------------|----------------------------------------|
| `{endpoint}/search`          | `?keywords=…&type=100`                  | Artist ID resolution                   |
| `{endpoint}/search`          | `?keywords=…&type=1`                    | Song ID resolution (lyrics)            |
| `{endpoint}/artist/detail`   | `?id=…`                                 | Biography, images                      |
| `{endpoint}/simi/artist`     | `?id=…` (login required)                | Similar artists                        |
| `{endpoint}/artist/top/song` | `?id=…`                                 | Top songs                              |
| `{endpoint}/artist/album`    | `?id=…&limit=200`                       | Album artwork, album ID                |
| `{endpoint}/album`           | `?id=…`                                 | Album editorial notes                  |
| `{endpoint}/lyric/new`       | `?id=…` (fallback `/lyric?id=…`)        | LRC lyrics, Chinese translation        |

### Files

| File                           | Description                                       |
|--------------------------------|---------------------------------------------------|
| [main.go](main.go)             | Plugin implementation — all metadata capabilities |
| [artist.go](artist.go)         | Artist ID resolution, caching, and matching       |
| [album.go](album.go)           | Album matching and detail fetching                |
| [lyrics.go](lyrics.go)         | Song search, lyric fetching, and LRC normalization |
| [overrides.go](overrides.go)   | Artist ID overrides file loading (with aliases)   |
| [helpers.go](helpers.go)       | Config, HTTP, and KV store helpers                |
| [scripts/export_lyrics.py](scripts/export_lyrics.py) | 把缓存的歌词导出并写回音乐库（.lrc/标签） |
| [example/artist-ids.json](example/artist-ids.json) | 艺术家 ID 映射文件示例（多别名写法） |
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
