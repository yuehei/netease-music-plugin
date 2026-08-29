#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从插件缓存导出艺术家头像，写入自定义头像目录。

从 Navidrome 插件 KVStore（SQLite：<DataFolder>/plugins/<插件名>/kvstore.db）
中读取所有 artist:<艺术家名> 的 ID 映射（{"artistId": N}，0 为负缓存），
逐个请求远程 netease_cloud_music_api 的 /artist/detail 接口；
仅当接口返回的艺术家名称与缓存键中的名称完全匹配（忽略大小写与首尾
空白，与插件 normalizeName 规则一致）时，下载其头像（avatar 优先，
缺失时回退 cover）写入指定目录，文件名为艺术家名称，扩展名取自
响应 Content-Type。

用法：
  python3 scripts/export_artist_avatars.py \
    --db    /var/lib/navidrome/plugins/<插件名>/kvstore.db \
    --api   http://api.example.com:3000 \
    --output /path/to/avatars \
    [--force] [--prune-invalid]

说明：
  - --db / --api / --output 均为必填；--api 为 netease_cloud_music_api
    服务根地址（如 http://api.example.com:3000）。
  - 默认跳过目录中已存在的同名头像（.jpg/.jpeg/.png/.webp/.gif），
    --force 强制重新下载覆盖。
  - --prune-invalid 清理无效映射：当缓存的艺术家名称与接口返回的名称
    不一致时，从 KVStore 中删除该 artist:<名称> 条目（下次扫描时插件
    会重新解析）。会写入数据库，建议在 Navidrome 停止或空闲时使用；
    只读导出（不带本参数）在 Navidrome 运行期间也是安全的。
  - KVStore 使用 SQLite WAL 模式，Navidrome 运行期间也可安全只读导出。
"""

import argparse
import json
import sqlite3
import sys
import urllib.error
import urllib.request
from pathlib import Path

# 与插件 buildImageList 的尺寸无关：这里取原图 URL，扩展名优先由
# 下载响应的 Content-Type 决定，无法识别时回退 .jpg
CONTENT_TYPE_EXT = {
    "image/jpeg": ".jpg",
    "image/png": ".png",
    "image/webp": ".webp",
    "image/gif": ".gif",
}
KNOWN_EXTS = (".jpg", ".jpeg", ".png", ".webp", ".gif")
REQUEST_TIMEOUT = 30  # 秒


def parse_args():
    parser = argparse.ArgumentParser(
        description="从插件缓存提取艺术家 ID 映射，经远程 API 校验名称后导出头像"
    )
    parser.add_argument("--db", required=True,
                        help="插件 KVStore 文件路径，即 <navidrome数据目录>/plugins/<插件名>/kvstore.db")
    parser.add_argument("--api", required=True,
                        help="netease_cloud_music_api 服务根地址，如 http://api.example.com:3000")
    parser.add_argument("--output", required=True,
                        help="自定义头像输出目录（不存在时自动创建）")
    parser.add_argument("--force", action="store_true",
                        help="覆盖目录中已存在的同名头像文件")
    parser.add_argument("--prune-invalid", action="store_true",
                        help="清理无效映射：缓存名称与接口返回名称不一致时，"
                             "从 KVStore 删除该 artist:* 条目（会写入数据库）")
    return parser.parse_args()


def normalize_name(name: str) -> str:
    """与插件 normalizeName（helpers.go，strings.ToLower(strings.TrimSpace)）一致：
    去首尾 Unicode 空白 + 转小写。缓存键本身已按此规则归一化，此处用于
    归一化接口返回的艺术家名称后再做完全匹配比较。
    """
    return name.strip().lower()


def sanitize_filename(name: str) -> str:
    """去掉文件名中的路径分隔符与空字符，避免越出输出目录。"""
    for ch in ("/", "\\", "\0"):
        name = name.replace(ch, "_")
    return name.strip()


def load_artist_mappings(db_path: Path):
    """读取 kvstore.db 中所有 artist:<名称> 条目，返回 [(名称, artistId)]。

    负缓存（artistId == 0，表示"确认不存在"）被剔除。
    """
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        rows = conn.execute(
            "SELECT key, value FROM kvstore WHERE key LIKE 'artist:%'"
        ).fetchall()
    finally:
        conn.close()

    mappings = []
    for key, value in rows:
        name = key.split(":", 1)[1]
        try:
            entry = json.loads(value)
            artist_id = int(entry["artistId"])
        except (json.JSONDecodeError, TypeError, KeyError, ValueError):
            print(f"  跳过 {key}：无法解析缓存 JSON", file=sys.stderr)
            continue
        if artist_id <= 0:
            continue  # 负缓存：插件已确认网易云无此艺术家
        mappings.append((name, artist_id))
    return mappings


def fetch_artist_detail(api_base: str, artist_id: int):
    """请求 /artist/detail，返回 data.artist 字典；失败返回 None。"""
    url = f"{api_base}/artist/detail?id={artist_id}"
    req = urllib.request.Request(url, headers={"User-Agent": "netease-music-plugin/export-avatars"})
    try:
        with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
            body = json.loads(resp.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, UnicodeDecodeError, json.JSONDecodeError) as e:
        print(f"  请求失败 artistId={artist_id}: {e}", file=sys.stderr)
        return None
    if body.get("code") != 200:
        print(f"  跳过 artistId={artist_id}：接口返回 code={body.get('code')}", file=sys.stderr)
        return None
    artist = (body.get("data") or {}).get("artist")
    return artist if isinstance(artist, dict) else None


def download_image(url: str):
    """下载图片，返回 (bytes, 扩展名)；失败返回 None。"""
    req = urllib.request.Request(url, headers={"User-Agent": "netease-music-plugin/export-avatars"})
    try:
        with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
            data = resp.read()
            content_type = (resp.headers.get("Content-Type") or "").split(";")[0].strip().lower()
    except (urllib.error.URLError, TimeoutError) as e:
        print(f"  下载失败 {url}: {e}", file=sys.stderr)
        return None
    ext = CONTENT_TYPE_EXT.get(content_type)
    if ext is None:
        # 回退：从 URL 路径后缀推断，仍无法识别则默认 .jpg
        suffix = Path(url.split("?")[0]).suffix.lower()
        ext = suffix if suffix in KNOWN_EXTS else ".jpg"
    return data, ext


def existing_avatar(output: Path, name: str):
    """返回输出目录中该艺术家已存在的头像文件，不存在返回 None。"""
    for ext in KNOWN_EXTS:
        candidate = output / (name + ext)
        if candidate.is_file():
            return candidate
    return None


def prune_invalid_mapping(db_path: Path, name: str) -> bool:
    """从 KVStore 删除名称不匹配的 artist:<名称> 条目，返回是否成功。

    以读写方式连接（删除时才调用）。SQLite 默认 5s busy timeout 可吸收
    与 Navidrome 的偶发写冲突；持续锁冲突会报错并计入失败。
    """
    try:
        conn = sqlite3.connect(db_path, timeout=5)
        try:
            with conn:
                conn.execute("DELETE FROM kvstore WHERE key = ?", (f"artist:{name}",))
        finally:
            conn.close()
        return True
    except sqlite3.Error as e:
        print(f"  删除缓存失败 artist:{name}: {e}", file=sys.stderr)
        return False


def main():
    args = parse_args()
    db_path = Path(args.db).expanduser()
    api_base = args.api.strip().rstrip("/")
    output = Path(args.output).expanduser()
    if not db_path.is_file():
        sys.exit(f"错误：KVStore 文件不存在：{db_path}")

    mappings = load_artist_mappings(db_path)
    print(f"共 {len(mappings)} 条有效艺术家 ID 映射（负缓存已剔除）")
    if not mappings:
        return

    output.mkdir(parents=True, exist_ok=True)

    stats = {"written": 0, "skipped_existing": 0, "skipped_mismatch": 0,
             "pruned": 0, "failed": 0}
    for name, artist_id in mappings:
        artist = fetch_artist_detail(api_base, artist_id)
        if artist is None:
            stats["failed"] += 1
            continue

        api_name = (artist.get("name") or "").strip()
        # 完全匹配校验：接口名称与缓存键名称一致（忽略大小写与首尾空白）
        if not api_name or normalize_name(api_name) != normalize_name(name):
            print(f"  跳过 ID {artist_id}：接口名称 '{api_name}' 与缓存名称 '{name}' 不完全匹配",
                  file=sys.stderr)
            stats["skipped_mismatch"] += 1
            if args.prune_invalid:
                # 无效映射：删除后下次扫描插件会重新解析该艺术家
                if prune_invalid_mapping(db_path, name):
                    print(f"  已清理无效映射 artist:{name}（ID {artist_id}）")
                    stats["pruned"] += 1
                else:
                    stats["failed"] += 1
            continue

        # 头像优先 avatar，缺失时回退 cover（与插件 fetchArtistPage 一致）
        image_url = artist.get("avatar") or artist.get("cover")
        if not image_url:
            print(f"  跳过 '{api_name}'（ID {artist_id}）：接口未返回头像", file=sys.stderr)
            stats["failed"] += 1
            continue

        filename = sanitize_filename(api_name)
        if not args.force:
            existing = existing_avatar(output, filename)
            if existing is not None:
                print(f"  已存在，跳过：{existing.name}")
                stats["skipped_existing"] += 1
                continue

        result = download_image(image_url)
        if result is None:
            stats["failed"] += 1
            continue
        data, ext = result

        target = output / (filename + ext)
        try:
            target.write_bytes(data)
        except OSError as e:
            print(f"  写入失败 {target}: {e}", file=sys.stderr)
            stats["failed"] += 1
            continue
        print(f"  写入 {target.name}（{len(data)} 字节，来自 ID {artist_id}）")
        stats["written"] += 1

    print(f"完成：写入 {stats['written']}，已存在跳过 {stats['skipped_existing']}，"
          f"名称不匹配跳过 {stats['skipped_mismatch']}"
          + (f"，已清理 {stats['pruned']}" if args.prune_invalid else "")
          + f"，失败 {stats['failed']}")


if __name__ == "__main__":
    main()
