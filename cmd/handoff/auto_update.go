package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/open-grove/handoff/internal/config"
	"github.com/open-grove/handoff/internal/updater"
	skillbundle "github.com/open-grove/handoff/skills"
)

const (
	autoUpdateInterval = 24 * time.Hour
	autoUpdateLockTTL  = 10 * time.Minute
)

type autoUpdateClient interface {
	Check(context.Context, string) (updater.Result, error)
	Apply(context.Context, updater.Result, string) error
}

var (
	newAutoUpdateClient = func() autoUpdateClient {
		return updater.Client{Token: updater.GitHubToken()}
	}
	autoUpdateExecutable           = os.Executable
	autoUpdateReexec               = reexecUpdatedCLI
	autoUpdateSkillSync            = syncUpdatedSkill
	autoUpdateNow                  = time.Now
	autoUpdateStderr     io.Writer = os.Stderr
)

// maybeAutoUpdate performs quiet, cached update discovery before the commands
// that Agents use for an actual handoff. Status is written only to stderr so
// stdout remains the command's canonical machine-readable or shareable result.
// A failed check or update never blocks the requested handoff.
func maybeAutoUpdate(command string, commandArgs, originalArgs []string) bool {
	if !shouldAutoUpdate(command, commandArgs) {
		return false
	}

	now := autoUpdateNow().UTC()
	cache, fresh := readUpdateNoticeCache(now)
	if fresh {
		if !versionIsNewer(cache.LatestVersion, version) {
			return false
		}
		if cache.AutoUpdateVersion == cache.LatestVersion && elapsedWithin(cache.AutoUpdateAttemptedAt, now, autoUpdateInterval) {
			return false
		}
	}

	checkContext, cancelCheck := context.WithTimeout(context.Background(), 2*time.Second)
	client := newAutoUpdateClient()
	result, err := client.Check(checkContext, version)
	cancelCheck()
	if err != nil {
		_ = writeUpdateNoticeCache(updateNoticeCache{
			CheckedAt: now, CurrentVersion: version, LatestVersion: version, CheckFailed: true,
		})
		return false
	}

	cache = updateNoticeCache{
		CheckedAt:      now,
		CurrentVersion: version,
		LatestVersion:  result.LatestVersion,
		ReleaseURL:     result.ReleaseURL,
	}
	if !result.UpdateAvailable {
		_ = writeUpdateNoticeCache(cache)
		return false
	}

	releaseLock, locked := acquireAutoUpdateLock(now)
	if !locked {
		return false
	}
	lockReleased := false
	defer func() {
		if !lockReleased {
			releaseLock()
		}
	}()

	cache.AutoUpdateAttemptedAt = now
	cache.AutoUpdateVersion = result.LatestVersion
	_ = writeUpdateNoticeCache(cache)
	fmt.Fprintf(autoUpdateStderr, "Handoff 正在自动升级 v%s → v%s；完成后会继续本次交接…\n", result.CurrentVersion, result.LatestVersion)

	executable, err := autoUpdateExecutable()
	if err == nil {
		applyContext, cancelApply := context.WithTimeout(context.Background(), 2*time.Minute)
		err = client.Apply(applyContext, result, executable)
		cancelApply()
	}
	if err != nil {
		fmt.Fprintf(autoUpdateStderr, "Handoff 自动升级未完成：%s。继续使用 v%s 完成本次交接。\n", conciseUpdateError(err), version)
		return false
	}

	previousSkill, _ := skillbundle.Read("handoff")
	previousMetadata, _ := skillbundle.OpenAIYAML("handoff")
	syncContext, cancelSync := context.WithTimeout(context.Background(), 30*time.Second)
	skillSync := autoUpdateSkillSync(syncContext, executable, previousSkill, previousMetadata)
	cancelSync()
	if len(skillSync.SkippedCustom) > 0 {
		fmt.Fprintf(autoUpdateStderr, "Handoff 已保留自定义 Agent Skill：%s。\n", strings.Join(skillSync.SkippedCustom, ", "))
	}
	if len(skillSync.Errors) > 0 {
		fmt.Fprintf(autoUpdateStderr, "Handoff Agent Skill 同步提示：%s。\n", strings.Join(skillSync.Errors, "; "))
	}

	cache.CheckedAt = autoUpdateNow().UTC()
	cache.CurrentVersion = result.LatestVersion
	cache.LatestVersion = result.LatestVersion
	_ = writeUpdateNoticeCache(cache)
	releaseLock()
	lockReleased = true

	fmt.Fprintf(autoUpdateStderr, "Handoff 已升级到 v%s，正在继续本次交接。\n", result.LatestVersion)
	environment := setEnvironmentValue(os.Environ(), "HANDOFF_AUTO_UPDATE_REEXEC", "1")
	if err := autoUpdateReexec(executable, originalArgs, environment); err != nil {
		fmt.Fprintf(autoUpdateStderr, "Handoff 新版本暂时无法接管本次命令：%s。继续使用当前进程完成交接。\n", conciseUpdateError(err))
		return false
	}
	return true
}

func shouldAutoUpdate(command string, commandArgs []string) bool {
	if runtime.GOOS == "windows" || strings.TrimSpace(os.Getenv("HANDOFF_NO_AUTO_UPDATE")) != "" || strings.TrimSpace(os.Getenv("HANDOFF_AUTO_UPDATE_REEXEC")) != "" {
		return false
	}
	if len(commandArgs) == 0 || wantsHelp(commandArgs) || booleanArgumentEnabled(commandArgs, "--dry-run") {
		return false
	}
	switch command {
	case "create", "receive", "context":
	case "session":
		if commandArgs[0] != "locate" {
			return false
		}
	default:
		return false
	}
	if truthyEnvironment("HANDOFF_AUTO_UPDATE") {
		return true
	}
	for _, name := range []string{
		"CODEX_THREAD_ID",
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
		"PI_CODING_AGENT_SESSION", "PI_SESSION_ID",
		"OPENCODE",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func truthyEnvironment(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func booleanArgumentEnabled(args []string, expected string) bool {
	for _, argument := range args {
		if argument == expected {
			return true
		}
		if strings.HasPrefix(argument, expected+"=") {
			value, err := strconv.ParseBool(strings.TrimPrefix(argument, expected+"="))
			if err == nil && value {
				return true
			}
		}
	}
	return false
}

func elapsedWithin(then, now time.Time, interval time.Duration) bool {
	if then.IsZero() {
		return false
	}
	elapsed := now.Sub(then)
	return elapsed >= 0 && elapsed < interval
}

func updateNoticeCachePath() (string, error) {
	configPath, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), "update-notice.json"), nil
}

func readUpdateNoticeCache(now time.Time) (updateNoticeCache, bool) {
	cache, ok := loadUpdateNoticeCache()
	if !ok || cache.CurrentVersion != version {
		return updateNoticeCache{}, false
	}
	return cache, elapsedWithin(cache.CheckedAt, now, autoUpdateInterval)
}

func loadUpdateNoticeCache() (updateNoticeCache, bool) {
	path, err := updateNoticeCachePath()
	if err != nil {
		return updateNoticeCache{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return updateNoticeCache{}, false
	}
	var cache updateNoticeCache
	if json.Unmarshal(data, &cache) != nil {
		return updateNoticeCache{}, false
	}
	return cache, true
}

func writeUpdateNoticeCache(cache updateNoticeCache) error {
	path, err := updateNoticeCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".update-notice-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func acquireAutoUpdateLock(now time.Time) (func(), bool) {
	cachePath, err := updateNoticeCachePath()
	if err != nil {
		return func() {}, false
	}
	lockPath := filepath.Join(filepath.Dir(cachePath), "auto-update.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return func() {}, false
	}
	for attempt := 0; attempt < 2; attempt++ {
		file, openErr := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			released := false
			return func() {
				if !released {
					released = true
					_ = os.Remove(lockPath)
				}
			}, true
		}
		info, statErr := os.Stat(lockPath)
		if !os.IsExist(openErr) || statErr != nil || now.Sub(info.ModTime()) < autoUpdateLockTTL {
			return func() {}, false
		}
		_ = os.Remove(lockPath)
	}
	return func() {}, false
}

func conciseUpdateError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		return message[:240] + "…"
	}
	return message
}

func setEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	replaced := false
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			if !replaced {
				result = append(result, prefix+value)
				replaced = true
			}
			continue
		}
		result = append(result, item)
	}
	if !replaced {
		result = append(result, prefix+value)
	}
	return result
}
