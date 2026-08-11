package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-grove/handoff/internal/types"
	"github.com/open-grove/handoff/internal/updater"
	skillbundle "github.com/open-grove/handoff/skills"
)

type fakeAutoUpdateClient struct {
	result     updater.Result
	checkErr   error
	applyErr   error
	checkCalls int
	applyCalls int
}

func (client *fakeAutoUpdateClient) Check(context.Context, string) (updater.Result, error) {
	client.checkCalls++
	return client.result, client.checkErr
}

func (client *fakeAutoUpdateClient) Apply(context.Context, updater.Result, string) error {
	client.applyCalls++
	return client.applyErr
}

func TestAgentHandoffAutomaticallyUpdatesAndReexecutesWithoutStdoutNoise(t *testing.T) {
	client, status := prepareAutoUpdateTest(t, updater.Result{
		CurrentVersion: version, LatestVersion: "99.0.0", UpdateAvailable: true,
	})
	var reexecPath string
	var reexecArgs, reexecEnvironment []string
	autoUpdateReexec = func(path string, args, environment []string) error {
		reexecPath = path
		reexecArgs = append([]string(nil), args...)
		reexecEnvironment = append([]string(nil), environment...)
		return nil
	}
	syncCalls := 0
	autoUpdateSkillSync = func(context.Context, string, string, string) skillSyncResult {
		syncCalls++
		return skillSyncResult{Installed: []string{"codex", "claude", "agents"}}
	}

	original := []string{"--json", "create", "continue"}
	if handled := maybeAutoUpdate("create", []string{"continue"}, original); !handled {
		t.Fatal("updated CLI did not take over the command")
	}
	if client.checkCalls != 1 || client.applyCalls != 1 || syncCalls != 1 {
		t.Fatalf("update calls: check=%d apply=%d sync=%d", client.checkCalls, client.applyCalls, syncCalls)
	}
	if reexecPath != "/tmp/test-handoff" || strings.Join(reexecArgs, "\x00") != strings.Join(original, "\x00") {
		t.Fatalf("reexec = %q %#v", reexecPath, reexecArgs)
	}
	if !environmentContains(reexecEnvironment, "HANDOFF_AUTO_UPDATE_REEXEC=1") {
		t.Fatalf("reexec guard missing from environment: %#v", reexecEnvironment)
	}
	for _, expected := range []string{"正在自动升级", "已升级到 v99.0.0", "正在继续本次交接"} {
		if !strings.Contains(status.String(), expected) {
			t.Fatalf("status missing %q: %s", expected, status.String())
		}
	}
	cachePath, err := updateNoticeCachePath()
	if err != nil {
		t.Fatal(err)
	}
	cacheData, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var cache updateNoticeCache
	if err := json.Unmarshal(cacheData, &cache); err != nil {
		t.Fatal(err)
	}
	if cache.CurrentVersion != "99.0.0" || cache.LatestVersion != "99.0.0" {
		t.Fatalf("unexpected post-update cache: %#v", cache)
	}
}

func TestAgentHandoffUpdateFailureContinuesAndBacksOff(t *testing.T) {
	client, status := prepareAutoUpdateTest(t, updater.Result{
		CurrentVersion: version, LatestVersion: "99.0.0", UpdateAvailable: true,
	})
	client.applyErr = errors.New("read-only installation")
	reexecCalls := 0
	autoUpdateReexec = func(string, []string, []string) error {
		reexecCalls++
		return nil
	}

	if handled := maybeAutoUpdate("receive", []string{"abcdefghijklmnopqrstuv"}, []string{"receive", "abcdefghijklmnopqrstuv"}); handled {
		t.Fatal("failed update incorrectly consumed the handoff command")
	}
	if client.checkCalls != 1 || client.applyCalls != 1 || reexecCalls != 0 {
		t.Fatalf("first attempt calls: check=%d apply=%d reexec=%d", client.checkCalls, client.applyCalls, reexecCalls)
	}
	if !strings.Contains(status.String(), "继续使用 v"+version+" 完成本次交接") {
		t.Fatalf("missing graceful fallback status: %s", status.String())
	}

	if handled := maybeAutoUpdate("receive", []string{"abcdefghijklmnopqrstuv"}, []string{"receive", "abcdefghijklmnopqrstuv"}); handled {
		t.Fatal("backed-off update consumed the handoff command")
	}
	if client.checkCalls != 1 || client.applyCalls != 1 {
		t.Fatalf("failed update was retried immediately: check=%d apply=%d", client.checkCalls, client.applyCalls)
	}
}

func TestAgentHandoffUpdateCheckIsCachedAndSilentWhenCurrent(t *testing.T) {
	client, status := prepareAutoUpdateTest(t, updater.Result{
		CurrentVersion: version, LatestVersion: version, UpdateAvailable: false,
	})
	for range 2 {
		if handled := maybeAutoUpdate("context", []string{"abcdefghijklmnopqrstuv"}, []string{"context", "abcdefghijklmnopqrstuv"}); handled {
			t.Fatal("up-to-date check consumed the handoff command")
		}
	}
	if client.checkCalls != 1 || client.applyCalls != 0 {
		t.Fatalf("cached check calls: check=%d apply=%d", client.checkCalls, client.applyCalls)
	}
	if status.Len() != 0 {
		t.Fatalf("up-to-date check produced user-visible noise: %q", status.String())
	}
}

func TestAgentHandoffUpdateCheckFailureIsSilentAndBackedOff(t *testing.T) {
	client, status := prepareAutoUpdateTest(t, updater.Result{})
	client.checkErr = errors.New("offline")
	for range 2 {
		if handled := maybeAutoUpdate("create", []string{"continue"}, []string{"create", "continue"}); handled {
			t.Fatal("failed update check consumed the handoff command")
		}
	}
	if client.checkCalls != 1 || client.applyCalls != 0 {
		t.Fatalf("failed check was not backed off: check=%d apply=%d", client.checkCalls, client.applyCalls)
	}
	if status.Len() != 0 {
		t.Fatalf("failed background check produced user-visible noise: %q", status.String())
	}
}

func TestAutoUpdateEligibilityProtectsNonHandoffAndDryRunCommands(t *testing.T) {
	prepareAutoUpdateEnvironment(t)
	for _, test := range []struct {
		command string
		args    []string
	}{
		{command: "update"},
		{command: "version"},
		{command: "create"},
		{command: "create", args: []string{"continue", "--dry-run"}},
		{command: "create", args: []string{"continue", "--dry-run=true"}},
		{command: "receive", args: []string{"--help"}},
		{command: "session", args: []string{"unknown"}},
	} {
		if shouldAutoUpdate(test.command, test.args) {
			t.Fatalf("auto update unexpectedly enabled for %s %#v", test.command, test.args)
		}
	}
	if !shouldAutoUpdate("create", []string{"continue"}) {
		t.Fatal("Agent create did not enable auto update")
	}
	if !shouldAutoUpdate("create", []string{"continue", "--dry-run=false"}) {
		t.Fatal("explicitly disabled dry-run unexpectedly disabled auto update")
	}
	if !shouldAutoUpdate("session", []string{"locate"}) {
		t.Fatal("Agent session locate did not enable auto update")
	}
	t.Setenv("HANDOFF_NO_AUTO_UPDATE", "1")
	if shouldAutoUpdate("create", []string{"continue"}) {
		t.Fatal("HANDOFF_NO_AUTO_UPDATE did not disable auto update")
	}
}

func prepareAutoUpdateTest(t *testing.T, result updater.Result) (*fakeAutoUpdateClient, *bytes.Buffer) {
	t.Helper()
	prepareAutoUpdateEnvironment(t)
	client := &fakeAutoUpdateClient{result: result}
	status := &bytes.Buffer{}
	previousClient := newAutoUpdateClient
	previousExecutable := autoUpdateExecutable
	previousReexec := autoUpdateReexec
	previousSync := autoUpdateSkillSync
	previousNow := autoUpdateNow
	previousStderr := autoUpdateStderr
	newAutoUpdateClient = func() autoUpdateClient { return client }
	autoUpdateExecutable = func() (string, error) { return "/tmp/test-handoff", nil }
	autoUpdateSkillSync = func(context.Context, string, string, string) skillSyncResult { return skillSyncResult{} }
	autoUpdateNow = func() time.Time { return time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC) }
	autoUpdateStderr = status
	t.Cleanup(func() {
		newAutoUpdateClient = previousClient
		autoUpdateExecutable = previousExecutable
		autoUpdateReexec = previousReexec
		autoUpdateSkillSync = previousSync
		autoUpdateNow = previousNow
		autoUpdateStderr = previousStderr
	})
	return client, status
}

func prepareAutoUpdateEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("HANDOFF_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	for _, name := range []string{
		"HANDOFF_NO_AUTO_UPDATE", "HANDOFF_AUTO_UPDATE", "HANDOFF_AUTO_UPDATE_REEXEC",
		"CODEX_THREAD_ID", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
		"PI_CODING_AGENT_SESSION", "PI_SESSION_ID", "OPENCODE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("CODEX_THREAD_ID", "test-thread")
}

func environmentContains(environment []string, expected string) bool {
	for _, item := range environment {
		if item == expected {
			return true
		}
	}
	return false
}

func TestParseHandoffRef(t *testing.T) {
	id := "abcdefghijklmnopqrstuv"
	parsedID, server := parseHandoffRef("https://handoff.example/h/" + id)
	if parsedID != id || server != "https://handoff.example" {
		t.Fatalf("parsed (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef(id)
	if parsedID != id || server != "" {
		t.Fatalf("parsed (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("opengrove-handoff，分享码：" + id)
	if parsedID != id || server != "" {
		t.Fatalf("parsed branded reference (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("请使用 opengrove-handoff 读取内容，分享码：`" + id + "`")
	if parsedID != id || server != "" {
		t.Fatalf("parsed Agent instruction (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("请读取 `opengrove-handoff:" + id + "`")
	if parsedID != id || server != "" {
		t.Fatalf("parsed stable Agent reference (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("https://handoff.example/h/" + id + ".md")
	if parsedID != id || server != "https://handoff.example" {
		t.Fatalf("parsed Markdown URL (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("收到一条 opengrove-handoff 分享，分享交接：[https://handoff.example/h/" + id + "](https://handoff.example/h/" + id + ")")
	if parsedID != id || server != "https://handoff.example" {
		t.Fatalf("parsed notification (%q, %q)", parsedID, server)
	}
}

func TestParseHandoffRefRejectsInvalidValues(t *testing.T) {
	if id, _ := parseHandoffRef("short"); id != "" {
		t.Fatalf("accepted invalid id %q", id)
	}
}

func TestFormatShareMessageSeparatesHumanAndAgentInstructions(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 29, 19, 42, 0, 0, time.Local)
	result := types.CreateResponse{
		Handoff: types.Handoff{
			ID: "abcdefghijklmnopqrstuv", Goal: "完成 [CLI]\n部署", ExpiresAt: expiresAt,
		},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	message := formatShareMessage(result)
	for _, expected := range []string{
		"🖐️ **For Human**",
		"你收到一份 Handoff，请打开[完成 \\[CLI\\] 部署](https://handoff.openmau.com/h/abcdefghijklmnopqrstuv)查看。",
		"🤖 **For Agent**",
		"请使用 OpenGrove Handoff 读取：`opengrove-handoff:abcdefghijklmnopqrstuv`",
		"[查看安装方法](https://github.com/open-grove/handoff)",
		"有效期：" + expiresAt.Format(time.RFC3339),
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("share message missing %q:\n%s", expected, message)
		}
	}
}

func TestResolveCreateSelectionUsesPreferredVocabulary(t *testing.T) {
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: "codex", Generator: "agent", Runtime: "claude", AttachContext: true,
		Set: map[string]bool{"source": true, "generator": true, "runtime": true, "attach-context": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Source != "codex" || selection.Generator != "agent" || selection.Runtime != "claude" || !selection.AttachContext || selection.SessionPath || len(selection.Deprecated) != 0 {
		t.Fatalf("unexpected preferred selection: %#v", selection)
	}
}

func TestRuntimeOnlyAppliesToAgentGenerator(t *testing.T) {
	for _, generator := range []string{"deterministic", "cloud"} {
		_, err := resolveCreateSelection(createSelectionInput{
			Source: "auto", Generator: generator, Runtime: "opencode",
			Set: map[string]bool{"generator": true, "runtime": true},
		})
		if err == nil || !strings.Contains(err.Error(), "only selects the local sidecar") {
			t.Fatalf("%s generator accepted an unrelated runtime: %v", generator, err)
		}
	}
}

func TestSourceSessionCannotBeCombinedWithFileInput(t *testing.T) {
	err := runCreate("", "text", []string{
		"Conflicting inputs",
		"--source", "codex",
		"--file", filepath.Join(t.TempDir(), "context.md"),
	})
	if err == nil || !strings.Contains(err.Error(), "--source selects an Agent Session and cannot be combined with --file") {
		t.Fatalf("conflicting Session and file inputs were not rejected clearly: %v", err)
	}
}

func TestDeterministicGenerationRequiresScopedInputOrReview(t *testing.T) {
	for _, sourceName := range []string{"codex", "claude", "pi", "opencode"} {
		err := validateDeterministicScope(types.Context{Source: sourceName}, false)
		if err == nil || !strings.Contains(err.Error(), "goal does not limit source context") {
			t.Fatalf("unscoped %s session was accepted: %v", sourceName, err)
		}
		if err := validateDeterministicScope(types.Context{Source: sourceName}, true); err != nil {
			t.Fatalf("reviewed %s session was rejected: %v", sourceName, err)
		}
	}
	for _, sourceName := range []string{"file", "stdin"} {
		if err := validateDeterministicScope(types.Context{Source: sourceName}, false); err != nil {
			t.Fatalf("scoped %s input was rejected: %v", sourceName, err)
		}
	}
}

func TestCreateDoesNotPublishDeterministicFallbackWhenAgentGenerationFails(t *testing.T) {
	t.Setenv("HANDOFF_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("CODEX_THREAD_ID", "test-thread")
	for _, name := range []string{
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
		"PI_CODING_AGENT_SESSION", "PI_SESSION_ID", "OPENCODE",
	} {
		t.Setenv(name, "")
	}

	fakeBin := t.TempDir()
	fakeCodex := filepath.Join(fakeBin, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\necho 'provider unavailable' >&2\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	sourcePath := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(sourcePath, []byte("# Source\n\nKnown context.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCreate("", "text", []string{
		"Agent generation failure",
		"--intent", "share",
		"--file", sourcePath,
		"--no-git",
	})
	if err == nil {
		t.Fatal("Agent generation failure silently published deterministic output")
	}
	for _, expected := range []string{
		"local Agent sidecar was found but could not generate the handoff",
		"codex sidecar handoff generation failed",
		"provider unavailable",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("generation error missing %q: %v", expected, err)
		}
	}
}

func TestCreateDoesNotFallbackWhenExplicitAgentRuntimeIsMissing(t *testing.T) {
	t.Setenv("HANDOFF_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("PATH", t.TempDir())

	sourcePath := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(sourcePath, []byte("# Source\n\nKnown context.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runCreate("", "text", []string{
		"OpenCode required",
		"--intent", "share",
		"--file", sourcePath,
		"--runtime", "opencode",
		"--no-git",
	})
	if err == nil {
		t.Fatal("missing explicit OpenCode runtime silently published deterministic output")
	}
	for _, expected := range []string{
		"local Agent sidecar runtime could not be resolved",
		"requested opencode sidecar CLI was not found",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("runtime resolution error missing %q: %v", expected, err)
		}
	}
}

func TestResolveCreateSelectionAcceptsOpenCode(t *testing.T) {
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: "opencode", Generator: "agent", Runtime: "opencode",
		Set: map[string]bool{"source": true, "runtime": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Source != "opencode" || selection.Runtime != "opencode" {
		t.Fatalf("unexpected OpenCode selection: %#v", selection)
	}
}

func TestResolveCreateSelectionMapsLegacyVocabulary(t *testing.T) {
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: "auto", Generator: "agent", Runtime: "auto",
		LegacyFrom: "pi", LegacyMode: "server", LegacyAgent: "codex", LegacyIncludeTranscript: true,
		Set: map[string]bool{"from": true, "mode": true, "agent": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Source != "pi" || selection.Generator != "cloud" || selection.Runtime != "codex" || selection.UploadContext != "selected" || len(selection.Deprecated) < 4 {
		t.Fatalf("unexpected compatibility selection: %#v", selection)
	}
}

func TestResolveCreateSelectionRejectsConflicts(t *testing.T) {
	_, err := resolveCreateSelection(createSelectionInput{
		Source: "codex", Generator: "agent", Runtime: "auto",
		LegacyFrom: "pi",
		Set:        map[string]bool{"source": true, "from": true},
	})
	if err == nil {
		t.Fatal("expected preferred and legacy source conflict to fail")
	}
}

func TestFormatShareMessageUsesCompactTitle(t *testing.T) {
	result := types.CreateResponse{
		Handoff: types.Handoff{
			ID:        "abcdefghijklmnopqrstuv",
			Title:     "继续完成编辑部 0.1.12 发布",
			Goal:      "继续完成编辑部 0.1.12 发布：核实并安全修复所有权限问题",
			ExpiresAt: time.Now().Add(time.Hour),
		},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	message := formatShareMessage(result)
	if !strings.Contains(message, "[继续完成编辑部 0.1.12 发布]") || strings.Contains(message, "核实并安全修复") {
		t.Fatalf("share message did not use compact title:\n%s", message)
	}
}

func TestMarkdownLinkLabelFallback(t *testing.T) {
	if label := markdownLinkLabel(" \n\t"); label != "Handoff 交接" {
		t.Fatalf("fallback label = %q", label)
	}
}

func TestResolveCreateMode(t *testing.T) {
	for _, test := range []struct {
		mode, compact       string
		modeSet, compactSet bool
		want                string
	}{
		{mode: "agent", want: "agent"},
		{mode: "local", modeSet: true, want: "local"},
		{mode: "session", modeSet: true, want: "session"},
		{mode: "agent", compact: "current", compactSet: true, want: "agent"},
		{mode: "agent", compact: "none", compactSet: true, want: "local"},
		{mode: "agent", compact: "server", compactSet: true, want: "server"},
	} {
		got, err := resolveCreateMode(test.mode, test.compact, test.modeSet, test.compactSet)
		if err != nil || got != test.want {
			t.Fatalf("resolveCreateMode(%q, %q) = %q, %v; want %q", test.mode, test.compact, got, err, test.want)
		}
	}
	if _, err := resolveCreateMode("local", "server", true, true); err == nil {
		t.Fatal("expected conflicting mode flags to fail")
	}
}

func TestLocalSessionMessageIsExplicitlySameMachineOnly(t *testing.T) {
	message := formatLocalSessionMessage("continue", "codex", "/tmp/session.jsonl")
	for _, expected := range []string{"Local Session（仅限本机）", "/tmp/session.jsonl", "下一目标：continue", "不会上传", "不会生成分享码", "其他设备无法访问"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("local Session message missing %q:\n%s", expected, message)
		}
	}
}

func TestLocalSessionLocateRejectsDatabaseBackedOpenCode(t *testing.T) {
	err := runSession("", "text", []string{"locate", "--source", "opencode"})
	if err == nil || !strings.Contains(err.Error(), "database-backed") {
		t.Fatalf("unexpected OpenCode local Session result: %v", err)
	}
}

func TestFormatAttachedContextDistinguishesPortableContextFromRawSession(t *testing.T) {
	rendered := formatAttachedContext(types.ContextResponse{
		HandoffID: "abcdefghijklmnopqrstuv",
		Context: types.ContextAttachment{
			Source:        types.SourceRef{Kind: "codex"},
			NativeSummary: "auxiliary summary",
			Messages:      []types.Message{{Role: "user", Text: "continue"}},
			Redaction:     types.RedactionVersion,
		},
	})
	for _, expected := range []string{
		"opengrove-handoff:abcdefghijklmnopqrstuv",
		"Native Compact Summary (auxiliary)",
		"### USER",
		"continue",
		"不是包含工具结果和内部记录的原始 Provider Session",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("attached Context missing %q:\n%s", expected, rendered)
		}
	}
}

func TestCreateJSONIncludesCanonicalShareMessage(t *testing.T) {
	result := types.CreateResponse{
		Handoff: types.Handoff{
			ID: "abcdefghijklmnopqrstuv", Goal: "continue", ExpiresAt: time.Now().Add(time.Hour),
		},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	output := createCommandOutput(result, true, nil)
	if output["share_message"] != formatShareMessage(result) {
		t.Fatalf("share_message = %#v", output["share_message"])
	}
	if output["agent_reference"] != "opengrove-handoff:abcdefghijklmnopqrstuv" {
		t.Fatalf("agent_reference = %#v", output["agent_reference"])
	}
	if output["fallback_used"] != false {
		t.Fatalf("fallback_used = %#v", output["fallback_used"])
	}
	warningOutput := createCommandOutput(result, true, errors.New("agent failed"))
	if warningOutput["fallback_used"] != true || warningOutput["generation_warning"] != "agent failed" {
		t.Fatalf("fallback warning missing from JSON: %#v", warningOutput)
	}
}

func TestReadStdinContextRejectsOversizedInput(t *testing.T) {
	_, err := readStdinContext(bytes.NewReader(bytes.Repeat([]byte("x"), (4<<20)+1)), 4<<20)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 4 MiB limit") {
		t.Fatalf("oversized stdin was silently truncated: %v", err)
	}
}

func TestServerGenerationMustFailClosed(t *testing.T) {
	for _, preview := range []types.CompactPreviewResponse{
		{Generator: "deterministic", Warning: "Agent Plan timed out"},
		{Generator: "deterministic"},
	} {
		if err := validateServerPreview(preview); err == nil {
			t.Fatalf("accepted failed server preview: %#v", preview)
		}
	}
	if err := validateServerPreview(types.CompactPreviewResponse{Generator: "server:agent-plan"}); err != nil {
		t.Fatalf("rejected valid server preview: %v", err)
	}
}

func TestReviewSectionsAcceptsUnchangedDraft(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	input := types.Sections{
		HumanBackground: "Background", HumanStatus: "Ready", HumanTodos: []string{"Continue"},
		Context: "Known", CurrentState: "Ready", NextSteps: []string{"Continue"},
	}
	output, err := reviewSections(context.Background(), "continue", types.Context{Source: "stdin"}, input, "deterministic", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if output.Context != input.Context || output.CurrentState != input.CurrentState || len(output.NextSteps) != 1 {
		t.Fatalf("unexpected reviewed sections: %#v", output)
	}
}

func TestSchemaContracts(t *testing.T) {
	for _, command := range []string{
		"create", "session.locate", "receive", "context", "delete",
		"admin.login", "admin.status", "admin.logout",
		"config.show", "config.set-server", "doctor", "whoami", "update",
		"skills.list", "skills.read", "skills.install", "version",
		"auth", "config", "skills",
	} {
		contract, err := schemaContract(command)
		if err != nil {
			t.Fatalf("schemaContract(%q): %v", command, err)
		}
		if contract["inputSchema"] == nil || contract["outputSchema"] == nil || contract["_meta"] == nil {
			t.Fatalf("schemaContract(%q) is incomplete: %#v", command, contract)
		}
	}
	if _, err := schemaContract("missing"); err == nil {
		t.Fatal("expected unknown schema to fail")
	}
	create, _ := schemaContract("create")
	if create["_meta"].(map[string]any)["agent_auto_update"] == nil {
		t.Fatal("create schema does not disclose Agent auto-update behavior")
	}
	createProperties := create["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, property := range []string{"source", "runtime"} {
		enum := createProperties[property].(map[string]any)["enum"].([]string)
		if !strings.Contains(strings.Join(enum, ","), "opencode") {
			t.Fatalf("create schema %s enum does not include OpenCode: %#v", property, enum)
		}
	}
	if description := createProperties["runtime"].(map[string]any)["description"].(string); !strings.Contains(description, "sidecar") || !strings.Contains(description, "never selects the input source or model") {
		t.Fatalf("runtime schema does not explain the sidecar boundary: %q", description)
	}
	if description := createProperties["source"].(map[string]any)["description"].(string); !strings.Contains(description, "input Agent Session") {
		t.Fatalf("source schema does not explain the input boundary: %q", description)
	}
	for _, legacy := range []string{"upload_context", "mode", "from", "agent", "include_transcript", "full_session", "stdin", "compact"} {
		if _, exists := createProperties[legacy]; exists {
			t.Fatalf("preferred create schema exposes legacy property %q", legacy)
		}
	}
	receive, _ := schemaContract("receive")
	receiveRequired := receive["outputSchema"].(map[string]any)["required"].([]string)
	if strings.Contains(strings.Join(receiveRequired, ","), "share_message") {
		t.Fatal("receive output schema incorrectly reused create-only fields")
	}
}

func TestEmbeddedHandoffSkill(t *testing.T) {
	available := skillbundle.List()
	if len(available) != 1 || available[0].Name != "handoff" {
		t.Fatalf("unexpected embedded skills: %#v", available)
	}
	const description = "description: Package a discussion result or current work into a portable, immutable Handoff"
	content, ok := skillbundle.Read("handoff")
	if !ok || !strings.Contains(content, "name: handoff") ||
		!strings.Contains(content, description) ||
		!strings.Contains(content, "--intent share") ||
		!strings.Contains(content, "--intent continue") ||
		!strings.Contains(content, "Ask the smallest context-specific clarification") ||
		!strings.Contains(content, "Do not turn this into a fixed questionnaire") ||
		!strings.Contains(content, "--generator cloud") ||
		!strings.Contains(content, "--attach-context") ||
		!strings.Contains(content, "--source codex|claude|pi|opencode") ||
		!strings.Contains(content, "Keep these controls separate") ||
		!strings.Contains(content, "It never chooses the input source or model") ||
		!strings.Contains(content, "Keep the user interaction smaller than the CLI surface") ||
		!strings.Contains(content, "Use cloud generation only when the user explicitly requests it") ||
		!strings.Contains(content, "Add `--attach-context` only when the user explicitly asks") ||
		!strings.Contains(content, "automatically perform a cached update preflight") ||
		!strings.Contains(content, "HANDOFF_NO_AUTO_UPDATE=1") ||
		!strings.Contains(content, "handoff context <reference>") ||
		!strings.Contains(content, "handoff session locate") ||
		strings.Contains(content, "--upload-context selected") ||
		strings.Contains(content, "--mode server") {
		t.Fatalf("embedded skill is incomplete: ok=%v content=%q", ok, content)
	}
	metadata, ok := skillbundle.OpenAIYAML("handoff")
	if !ok ||
		!strings.Contains(metadata, `display_name: "OpenGrove Handoff"`) ||
		!strings.Contains(metadata, "share a discussion's conclusions and reasoning") {
		t.Fatalf("embedded Skill metadata is incomplete: ok=%v content=%q", ok, metadata)
	}
}

func TestDeleteRequiresStructuredConfirmation(t *testing.T) {
	err := runDelete("", "", []string{"abcdefghijklmnopqrstuv"})
	var structured *structuredError
	if !errors.As(err, &structured) {
		t.Fatalf("expected structured confirmation error, got %T: %v", err, err)
	}
	if structured.ExitCode != 10 {
		t.Fatalf("exit code = %d, want 10", structured.ExitCode)
	}
	envelope, _ := structured.Payload["error"].(map[string]any)
	if envelope["type"] != "confirmation_required" {
		t.Fatalf("unexpected error envelope: %#v", structured.Payload)
	}
}

func TestGlobalOutputFormatCanAppearBeforeOrAfterCommand(t *testing.T) {
	if _, format, err := extractOutputFormat([]string{"whoami"}); err != nil || format != "text" {
		t.Fatalf("default output format = %q, %v; want text", format, err)
	}
	for _, input := range [][]string{
		{"--json", "whoami"},
		{"whoami", "--json"},
		{"receive", "abcdefghijklmnopqrstuv", "--format=json"},
	} {
		cleaned, format, err := extractOutputFormat(input)
		if err != nil || format != "json" {
			t.Fatalf("extractOutputFormat(%v) = %v, %q, %v", input, cleaned, format, err)
		}
		for _, argument := range cleaned {
			if argument == "--json" || strings.HasPrefix(argument, "--format") {
				t.Fatalf("global output flag was not removed: %v", cleaned)
			}
		}
	}
	if _, _, err := extractOutputFormat([]string{"--json", "--format", "text", "version"}); err == nil {
		t.Fatal("expected conflicting formats to fail")
	}
}

func TestInstallSkillWritesSupportedAgentLocations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HANDOFF_SKILL_HOME", root)
	paths, err := installSkill("handoff", "skill content\n", "all", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("installed paths = %#v", paths)
	}
	for _, directory := range []string{".codex", ".claude", ".agents"} {
		path := filepath.Join(root, directory, "skills", "handoff", "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "skill content\n" {
			t.Fatalf("installed Skill %s = %q, %v", path, data, err)
		}
		metadataPath := filepath.Join(root, directory, "skills", "handoff", "agents", "openai.yaml")
		metadata, err := os.ReadFile(metadataPath)
		if err != nil || !strings.Contains(string(metadata), "OpenGrove Handoff") {
			t.Fatalf("installed Skill metadata %s = %q, %v", metadataPath, metadata, err)
		}
	}
	if _, err := installSkill("handoff", "different\n", "all", false); err == nil {
		t.Fatal("expected existing different Skill to require --force")
	}
}
