package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// artist_id_overrides 保存"艺术家名 → 网易云 ID"的映射。配置项的值是
// 音乐库内一个 JSON 文件的相对路径(如 "meta/artist-ids.json"),固定从默认
// 音乐库(library 1,挂载在沙箱 /libraries/1)读取。文件格式为 JSON 对象,
// 键为艺术家名称(支持以 ';' 分隔的多个别名,全部归一化后指向同一个网易云
// ID),值为数字或数字字符串,例如:
//
//	{ "周杰伦;Jay Chou": 6452, "Radiohead": "72161" }
//
// Navidrome 为**每次插件函数调用**创建一个新的 wasm 实例,实例内存互不共
// 享。因此解析结果通过宿主的 Cache 服务(进程内存,按插件隔离、全实例共
// 享)保存;KVStore 不用于此处——映射需要跟随文件变化快速失效,不适合持久
// 化。变更检测策略:配置的路径变化时立即重载;文件本身的 mtime/大小变化至
// 多每 60 秒 stat 检查一次(全实例共享的节流)。检测到变动并重载时写一条
// Info 日志。
type artistOverrides struct {
	mu      sync.Mutex
	loaded  bool      // 本实例已从 Cache/文件加载过状态
	path    string    // 配置的相对路径(当前状态对应的)
	absPath string    // 解析出的沙箱内绝对路径("" = 上次加载失败)
	modTime time.Time // 上次成功加载时文件的修改时间
	size    int64     // 上次成功加载时文件的大小
	lastChk time.Time // 上次 stat 检查时间(60s 内不重复检查文件变动)
	m       map[string]int64
}

// artistOverrideCache is the per-instance copy of the overrides state.
var artistOverrideCache artistOverrides

// overrideCheckInterval throttles how often the overrides file is stat-ed
// for changes: within the window lookups are served straight from the shared
// state. The configured-path comparison still happens on every lookup, so
// saving the plugin config keeps taking effect immediately.
const overrideCheckInterval = 60 * time.Second

// overrideStateKey / overrideStateTTL control the host-Cache entry that
// carries the parsed overrides state across wasm instances. TTL is only a
// safety net: expiry just means the next instance re-reads the file.
const (
	overrideStateKey = "overrides:state"
	overrideStateTTL = 600 // seconds
)

// overrideNow is the clock used for the check interval; overridable in tests.
var overrideNow = time.Now

// overrideState is the JSON payload shared through the host Cache.
type overrideState struct {
	Path      string           `json:"path"`
	AbsPath   string           `json:"absPath"`
	ModTime   int64            `json:"modTime"`   // unix nanos
	Size      int64            `json:"size"`
	LastCheck int64            `json:"lastCheck"` // unix nanos
	M         map[string]int64 `json:"m"`
}

// resolveOverrideMounts maps a library-relative path into the WASI sandbox
// mount of the DEFAULT music library: Navidrome mounts each library at
// /libraries/<id>, and the overrides file is only looked up under library 1
// (the default library). A package-level var so tests can point it at
// temp files.
var resolveOverrideMounts = func(rel string) []string {
	return []string{fmt.Sprintf("/libraries/1/%s", rel)}
}

// get returns the override ID for the normalized artist name, hydrating the
// shared state from the host Cache (or the file on first use) and rechecking
// the file for changes at most once per overrideCheckInterval.
func (o *artistOverrides) get(normalized string) (int64, bool) {
	rel := getArtistOverridesPath()

	o.mu.Lock()
	defer o.mu.Unlock()

	if rel == "" {
		o.reset()
		host.CacheRemove(overrideStateKey)
		return 0, false
	}

	// Ensure this instance holds the current state: hydrate from the host
	// Cache (shared across wasm instances), or read the file on first use /
	// Cache miss / TTL expiry.
	if !o.loaded && !o.loadShared() {
		o.loadFile(rel)
	}

	// Config path changed (plugin settings were saved): reload from the file
	// immediately, bypassing the interval.
	if o.path != rel {
		o.loadFile(rel)
		return o.lookup(normalized)
	}

	// Serve the shared state; at most once per interval, stat the file and
	// reload it when its mtime/size changed.
	if overrideNow().Sub(o.lastChk) < overrideCheckInterval {
		return o.lookup(normalized)
	}
	if o.absPath != "" {
		if fi, err := os.Stat(o.absPath); err == nil &&
			fi.ModTime().Equal(o.modTime) && fi.Size() == o.size {
			o.lastChk = overrideNow()
			o.saveShared()
			return o.lookup(normalized)
		}
	}
	o.loadFile(rel)
	return o.lookup(normalized)
}

func (o *artistOverrides) lookup(normalized string) (int64, bool) {
	id, ok := o.m[normalized]
	return id, ok
}

func (o *artistOverrides) reset() {
	o.loaded = false
	o.path, o.absPath, o.modTime, o.size, o.lastChk = "", "", time.Time{}, 0, time.Time{}
	o.m = nil
}

// loadShared hydrates the state from the host Cache. Returns false when no
// shared state exists (first use after startup or Cache TTL expiry).
func (o *artistOverrides) loadShared() bool {
	data, ok, err := host.CacheGetBytes(overrideStateKey)
	if err != nil {
		pdk.Log(pdk.LogWarn, "overrides cache read failed: "+err.Error())
		return false
	}
	if !ok || len(data) == 0 {
		return false
	}
	var st overrideState
	if err := json.Unmarshal(data, &st); err != nil {
		pdk.Log(pdk.LogWarn, "overrides cache payload invalid: "+err.Error())
		return false
	}
	o.loaded = true
	o.path = st.Path
	o.absPath = st.AbsPath
	o.modTime = time.Unix(0, st.ModTime)
	o.size = st.Size
	o.lastChk = time.Unix(0, st.LastCheck)
	o.m = st.M
	pdk.Log(pdk.LogDebug, "artist overrides hydrated from host cache")
	return true
}

// saveShared publishes the current state to the host Cache so other wasm
// instances (and future calls) reuse it without touching the file.
func (o *artistOverrides) saveShared() {
	st := overrideState{
		Path:      o.path,
		AbsPath:   o.absPath,
		ModTime:   o.modTime.UnixNano(),
		Size:      o.size,
		LastCheck: o.lastChk.UnixNano(),
		M:         o.m,
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := host.CacheSetBytes(overrideStateKey, data, overrideStateTTL); err != nil {
		pdk.Log(pdk.LogWarn, "overrides cache write failed: "+err.Error())
	}
}

// getArtistOverridesPath returns the trimmed config value ("" = unset).
func getArtistOverridesPath() string {
	val, exists := host.ConfigGet(configArtistIDOverride)
	if !exists {
		return ""
	}
	return strings.TrimSpace(val)
}

// loadFile (re)reads the overrides file from the default library mount and
// publishes the new state to the host Cache. On failure the state is emptied
// so no stale ID is served, but lastChk is still advanced (and shared): the
// next instance retries only after the check interval — self-healing once
// the file appears, without stat hammering. A successful reload after a
// previous load logs an Info entry describing the change.
func (o *artistOverrides) loadFile(rel string) {
	hadPrevious := o.m != nil
	prevLen := len(o.m)

	o.path = rel
	o.absPath, o.modTime, o.size, o.m = "", time.Time{}, 0, nil
	o.lastChk = overrideNow()

	for _, abs := range resolveOverrideMounts(rel) {
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("failed to read artist overrides file %s: %v", abs, err))
			break
		}
		m, err := parseArtistOverrides(data)
		if err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("invalid artist overrides file %s: %v", abs, err))
			break
		}
		o.absPath = abs
		o.modTime = fi.ModTime()
		o.size = fi.Size()
		o.m = m
		if hadPrevious {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("artist overrides changed, reloaded %d entries from %s (previously %d)", len(m), abs, prevLen))
		} else {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("loaded %d artist ID overrides from %s", len(m), abs))
		}
		break
	}
	if o.m == nil {
		pdk.Log(pdk.LogDebug, "artist overrides file not found in the default library: "+rel)
	}
	o.loaded = true
	o.saveShared()
}

// parseArtistOverrides 解析映射文件:键支持以 ';' 分隔的多个别名,全部
// 归一化(忽略大小写、去首尾空白)后指向同一个网易云 ID;值为数字或
// 数字字符串,非法条目忽略并告警。
func parseArtistOverrides(data []byte) (map[string]int64, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(raw))
	for name, v := range raw {
		id := parseArtistIDValue(v)
		if id <= 0 {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("invalid artist ID in override for '%s'", name))
			continue
		}
		for _, alias := range strings.Split(name, ";") {
			if key := normalizeName(alias); key != "" {
				m[key] = id
			}
		}
	}
	return m, nil
}

// parseArtistIDValue accepts a JSON number or a digit string as artist ID.
func parseArtistIDValue(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil {
			return 0
		}
		return id
	}
	return 0
}
