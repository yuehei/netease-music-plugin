#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""导出 netease-music-plugin 缓存的歌词，写回音乐库。

从 Navidrome 插件 KVStore（SQLite：<DataFolder>/plugins/<插件名>/kvstore.db）
中读取所有 lyrics:<songID> 缓存条目，根据缓存里记录的曲目相对路径 path，
把歌词写回音乐库：
  - lrc  模式：在音频文件同目录生成同名 .lrc 文件
  - tags 模式：把 LRC 歌词内嵌进音频文件标签（需要 mutagen：
               mp3 → USLT，flac/ogg → LYRICS，m4a/mp4 → ©lyr）

用法：
  python3 scripts/export_lyrics.py \
    --db      /var/lib/navidrome/plugins/<插件名>/kvstore.db \
    --library /music \
    [--mode lrc|tags|both] [--include-translated] [--dry-run] [--force]

说明：
  - --db 与 --library 均为必填。
  - KVStore 使用 SQLite WAL 模式，Navidrome 运行期间也可安全只读导出。
  - 默认跳过已存在的 .lrc / 已有歌词标签的文件，--force 强制覆盖。
  - --dry-run 只打印将要执行的操作，不写任何文件。
"""

import argparse
import json
import sqlite3
import sys
from pathlib import Path


def parse_args():
    parser = argparse.ArgumentParser(
        description="把 netease-music-plugin 缓存的歌词写回音乐库（.lrc 文件或音频标签）"
    )
    parser.add_argument("--db", required=True,
                        help="插件 KVStore 文件路径，即 <navidrome数据目录>/plugins/<插件名>/kvstore.db")
    parser.add_argument("--library", required=True,
                        help="音乐库根目录（即 Navidrome 中配置的 Library 路径）")
    parser.add_argument("--mode", choices=["lrc", "tags", "both"], default="lrc",
                        help="写回方式：lrc=同名 .lrc 文件（默认），tags=内嵌音频标签，both=两者")
    parser.add_argument("--include-translated", action="store_true",
                        help="把中文翻译歌词追加在原文后（空行隔开）")
    parser.add_argument("--dry-run", action="store_true",
                        help="只打印将要执行的操作，不写任何文件")
    parser.add_argument("--force", action="store_true",
                        help="覆盖已存在的 .lrc 文件或已有歌词标签")
    return parser.parse_args()


def load_cached_lyrics(db_path: Path):
    """读取 kvstore.db 中所有未过期的 lyrics:* 条目，返回 [(song_id, entry)]。"""
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        rows = conn.execute(
            "SELECT key, value FROM kvstore "
            "WHERE key LIKE 'lyrics:%' "
            "AND (expires_at IS NULL OR expires_at >= datetime('now'))"
        ).fetchall()
    finally:
        conn.close()

    entries = []
    for key, value in rows:
        try:
            entry = json.loads(value)
        except (json.JSONDecodeError, TypeError):
            print(f"  跳过 {key}：无法解析缓存 JSON", file=sys.stderr)
            continue
        entries.append((key.split(":", 1)[1], entry))
    return entries


def resolve_track(library: Path, rel_path: str):
    """把缓存里的库内相对路径解析为绝对路径，拒绝越出音乐库的路径。"""
    rel = Path(rel_path)
    if rel.is_absolute() or ".." in rel.parts:
        return None
    return library / rel


def lyric_text(entry: dict, include_translated: bool) -> str:
    text = entry.get("text", "")
    if include_translated and entry.get("translated"):
        text = f"{text}\n\n{entry['translated']}" if text else entry["translated"]
    return text


def write_lrc(track: Path, text: str, force: bool, dry_run: bool):
    """在音频文件同目录写同名 .lrc，返回 'written' / 'skipped' / 'failed'。"""
    lrc = track.with_suffix(".lrc")
    if lrc.exists() and not force:
        return "skipped"
    if dry_run:
        print(f"  [lrc] {lrc}")
        return "written"
    try:
        lrc.write_text(text, encoding="utf-8")
        return "written"
    except OSError as e:
        print(f"  写入失败 {lrc}: {e}", file=sys.stderr)
        return "failed"


def write_tags(track: Path, text: str, force: bool, dry_run: bool):
    """用 mutagen 把歌词内嵌进音频标签，返回 'written' / 'skipped' / 'failed'。"""
    if dry_run:
        # 不 import mutagen 也能 dry-run；已有标签是否存在此时无法判断，
        # 输出的是"计划写入"清单。
        print(f"  [tags] {track}")
        return "written"

    try:
        from mutagen import File as MutagenFile  # 延迟导入，仅在真正写标签时需要
    except ImportError:
        sys.exit("错误：tags 模式需要 mutagen，请先安装：pip install mutagen")

    try:
        audio = MutagenFile(track, easy=False)
        if audio is None:
            print(f"  跳过 {track}：mutagen 无法识别的格式", file=sys.stderr)
            return "skipped"

        kind = audio.mime[0] if audio.mime else ""
        if kind == "audio/mp3":
            from mutagen.id3 import USLT, ID3
            # 与其他格式保持一致：已有歌词标签且未指定 --force 时跳过
            if audio.tags is not None and audio.tags.getall("USLT") and not force:
                return "skipped"
            if audio.tags is None:
                audio.tags = ID3()
            # setall 整体替换 USLT，避免重复追加
            audio.tags.setall("USLT", [USLT(encoding=3, lang="chi", text=text)])
        elif kind in ("audio/flac", "audio/ogg", "audio/x-flac"):
            existing = audio.get("LYRICS")
            if existing and not force:
                return "skipped"
            audio["LYRICS"] = text
        elif kind in ("audio/mp4", "audio/x-m4a", "video/mp4"):
            existing = audio.get("\xa9lyr")
            if existing and not force:
                return "skipped"
            audio["\xa9lyr"] = text
        else:
            print(f"  跳过 {track}：暂不支持的格式 {kind}", file=sys.stderr)
            return "skipped"

        audio.save()
        return "written"
    except (OSError, KeyError, ValueError) as e:
        print(f"  写入失败 {track}: {e}", file=sys.stderr)
        return "failed"


def main():
    args = parse_args()
    db_path = Path(args.db).expanduser()
    library = Path(args.library).expanduser()
    if not db_path.is_file():
        sys.exit(f"错误：KVStore 文件不存在：{db_path}")
    if not library.is_dir():
        sys.exit(f"错误：音乐库目录不存在：{library}")

    entries = load_cached_lyrics(db_path)
    usable = [e for e in entries if e[1].get("text") and e[1].get("path")]
    print(f"共 {len(entries)} 条歌词缓存，其中 {len(usable)} 条含文本与路径，"
          f"{len(entries) - len(usable)} 条为负缓存或缺少路径（跳过）")
    if not usable:
        return

    stats = {"written": 0, "skipped": 0, "failed": 0}
    for song_id, entry in usable:
        track = resolve_track(library, entry["path"])
        if track is None or not track.is_file():
            print(f"  跳过 lyrics:{song_id}：文件不存在或路径非法 "
                  f"({entry['path']})", file=sys.stderr)
            stats["skipped"] += 1
            continue

        text = lyric_text(entry, args.include_translated)
        results = []
        if args.mode in ("lrc", "both"):
            results.append(write_lrc(track, text, args.force, args.dry_run))
        if args.mode in ("tags", "both"):
            results.append(write_tags(track, text, args.force, args.dry_run))
        for r in results:
            stats[r] += 1

    print(f"完成：写回 {stats['written']}，跳过 {stats['skipped']}，"
          f"失败 {stats['failed']}"
          + ("（dry-run，未实际写入）" if args.dry_run else ""))


if __name__ == "__main__":
    main()
