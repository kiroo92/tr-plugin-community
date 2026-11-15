package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/sdk"
	_ "modernc.org/sqlite"
)

type Plugin struct {
	mu                      sync.Mutex
	env                     *sdk.Env
	db                      *sql.DB
	settings                Settings
	token, chat, key, scope string
	client                  *http.Client
	telegramURL             string
	checkedAt               int64
	now                     func() time.Time
	cancel                  context.CancelFunc
	done                    chan struct{}
}

func New() *Plugin {
	return &Plugin{client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, telegramURL: "https://api.telegram.org", now: time.Now}
}

func (plugin *Plugin) Init(ctx context.Context, env *sdk.Env) error {
	if env == nil || strings.TrimSpace(env.DataDir) == "" || !filepath.IsAbs(env.DataDir) {
		return errors.New("需要支持持久化目录的 kiroo92 TokenRouter v0.7.3+：DataDir 必须为非空绝对路径")
	}
	info, err := os.Stat(env.DataDir)
	if err != nil || !info.IsDir() {
		return errors.New("插件持久化目录不可用")
	}
	probe, err := os.CreateTemp(env.DataDir, ".write-check-")
	if err != nil {
		return errors.New("插件持久化目录不可写")
	}
	probeName := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probeName)
	if closeErr != nil || removeErr != nil {
		return errors.New("插件持久化目录写入检查失败")
	}
	databasePath := filepath.Join(env.DataDir, "plugin.sqlite")
	if fileInfo, err := os.Lstat(databasePath); err == nil && (fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular()) {
		return errors.New("数据库路径必须为普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return errors.New("无法检查数据库路径")
	}
	databaseFile, err := os.OpenFile(databasePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("无法创建插件数据库")
	}
	permissionErr := databaseFile.Chmod(0600)
	fileCloseErr := databaseFile.Close()
	if permissionErr != nil || fileCloseErr != nil {
		return errors.New("无法设置数据库权限")
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return errors.New("无法打开插件数据库")
	}
	database.SetMaxOpenConns(1)
	plugin.db = database
	plugin.env = env
	fail := func(err error) error { database.Close(); plugin.db = nil; return err }
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "CREATE TABLE IF NOT EXISTS records (key TEXT PRIMARY KEY, value TEXT NOT NULL)"} {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fail(errors.New("无法初始化插件数据库"))
		}
	}
	plugin.settings = defaults()
	if _, err := plugin.load(ctx, "settings", &plugin.settings); err != nil {
		return fail(err)
	}
	if err := plugin.settings.validate(); err != nil {
		return fail(fmt.Errorf("已保存配置不可用：%w", err))
	}
	if err := plugin.credentials(env.Config); err != nil {
		return fail(err)
	}
	background, cancel := context.WithCancel(context.Background())
	plugin.cancel = cancel
	plugin.done = make(chan struct{})
	go plugin.loop(background)
	return nil
}

func (plugin *Plugin) credentials(config sdk.Config) error {
	token, chat := strings.TrimSpace(config.Secret("bot_token")), strings.TrimSpace(config.String("chat_id"))
	if token != "" || chat != "" {
		botID, secret, ok := strings.Cut(token, ":")
		identifier, err := strconv.ParseInt(botID, 10, 64)
		if !ok || err != nil || identifier <= 0 || len(secret) < 10 || strings.IndexFunc(secret, func(character rune) bool {
			return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", character)
		}) >= 0 {
			return errors.New("请配置有效的 Bot Token")
		}
		groupID, err := strconv.ParseInt(chat, 10, 64)
		if err != nil || groupID >= 0 || !strings.HasPrefix(chat, "-100") {
			return errors.New("请配置 -100 开头的超级群组 ID（公开或私有均可）")
		}
	}
	botID, _, _ := strings.Cut(token, ":")
	sum := sha256.Sum256([]byte(botID + "/" + chat))
	plugin.scope = hex.EncodeToString(sum[:12])
	plugin.checkedAt = 0
	plugin.token = token
	plugin.chat = chat
	plugin.key = config.Secret("ai_key")
	return nil
}

func (plugin *Plugin) ConfigChanged(_ context.Context, config sdk.Config) error {
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	return plugin.credentials(config)
}

func (plugin *Plugin) Shutdown(ctx context.Context) error {
	if plugin.cancel != nil {
		plugin.cancel()
		select {
		case <-plugin.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	plugin.client.CloseIdleConnections()
	if plugin.db != nil {
		err := plugin.db.Close()
		plugin.db = nil
		return err
	}
	return nil
}

func (plugin *Plugin) load(ctx context.Context, key string, target any) (bool, error) {
	var raw string
	err := plugin.db.QueryRowContext(ctx, "SELECT value FROM records WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("读取插件数据失败")
	}
	if json.Unmarshal([]byte(raw), target) != nil {
		return false, errors.New("插件数据损坏，请恢复备份")
	}
	return true, nil
}
func (plugin *Plugin) save(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = plugin.db.ExecContext(ctx, "INSERT INTO records(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(raw))
	if err != nil {
		return errors.New("保存插件数据失败")
	}
	return nil
}
func (plugin *Plugin) scoped(key string) string { return plugin.scope + "/" + key }

type Event struct {
	At     string `json:"at"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (plugin *Plugin) event(ctx context.Context, title, detail string) error {
	var events []Event
	if _, err := plugin.load(ctx, plugin.scoped("events"), &events); err != nil {
		return err
	}
	events = append([]Event{{At: plugin.now().Format(time.RFC3339), Title: title, Detail: detail}}, events...)
	if len(events) > 20 {
		events = events[:20]
	}
	return plugin.save(ctx, plugin.scoped("events"), events)
}

func (plugin *Plugin) loop(ctx context.Context) {
	defer close(plugin.done)
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			plugin.mu.Lock()
			if plugin.token != "" {
				if err := plugin.poll(ctx); err != nil && ctx.Err() == nil {
					_ = plugin.event(ctx, "机器人轮询失败", err.Error())
				}
				if plugin.checkedAt > 0 && plugin.settings.Channels && plugin.settings.Interval > 0 && ctx.Err() == nil {
					var state ChannelState
					if _, err := plugin.load(ctx, plugin.scoped("channel"), &state); err == nil && plugin.now().Unix()-state.Sent >= int64(plugin.settings.Interval*60) {
						if err := plugin.broadcast(ctx, false); err != nil && ctx.Err() == nil {
							_ = plugin.event(ctx, "状态播报失败", err.Error())
						}
					}
				}
			}
			plugin.mu.Unlock()
		}
	}
}
