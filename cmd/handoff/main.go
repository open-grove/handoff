package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	agentruntime "github.com/open-grove/handoff/internal/agent"
	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/client"
	"github.com/open-grove/handoff/internal/config"
	"github.com/open-grove/handoff/internal/source"
	"github.com/open-grove/handoff/internal/types"
	skillbundle "github.com/open-grove/handoff/skills"
)

const version = skillbundle.Version

const installURL = "https://github.com/open-grove/handoff"

var brandedHandoffRef = regexp.MustCompile("(?i)opengrove-handoff\\s*(?:读取内容\\s*)?[,，]?\\s*(?:分享码|code)\\s*[:：]\\s*`?([A-Za-z0-9_-]{20,32})")
var embeddedHandoffURL = regexp.MustCompile(`https://[A-Za-z0-9.-]+(?::[0-9]+)?/h/[A-Za-z0-9_-]{20,32}(?:\.md)?`)

const usage = `handoff — portable context for people and agents.

AGENT QUICKSTART:
  handoff create "next goal"                 Auto-detect this workspace's latest Agent session
  agent-export | handoff create "next goal"  Create from stdin
  handoff receive <code>                     Print a received HANDOFF.md
  handoff schema create                      Inspect the create contract
  handoff skills read handoff                Read the version-matched Agent Skill

Usage:
  handoff [--profile NAME] <command> [flags]

Commands:
  create       Generate with the current Agent and share a handoff    Risk: write
  receive      Fetch a handoff by code or URL                         Risk: read
  delete       Delete a handoff before it expires                     Risk: high-risk-write
  auth         Login, status, and logout
  config       Show or update CLI configuration
  doctor       Check local source detection, auth, and connectivity   Risk: read
  schema       Print machine-readable command contracts               Risk: read
  skills       List or read Agent Skills embedded in this CLI          Risk: read
  version      Print the CLI version

Global flags:
  --profile NAME   Use a named configuration profile
  -h, --help       Show help

Context sources:
  auto (default), codex, claude, pi, piped stdin, or one or more --file values.

Generation modes:
  agent (default) reuses the current Agent's auth, config, and default model.
  local uses deterministic extraction. server requires --include-transcript.
  The source session is always read-only and is never compacted or resumed.
`

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var structured *structuredError
		if errors.As(err, &structured) {
			_ = writeJSON(os.Stderr, structured.Payload)
			os.Exit(structured.ExitCode)
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

type structuredError struct {
	ExitCode int
	Payload  map[string]any
}

func (err *structuredError) Error() string {
	if envelope, ok := err.Payload["error"].(map[string]any); ok {
		if message, ok := envelope["message"].(string); ok {
			return message
		}
	}
	return "command failed"
}

func run(args []string) error {
	root := flag.NewFlagSet("handoff", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	profileName := root.String("profile", "", "configuration profile")
	help := root.Bool("help", false, "show help")
	root.BoolVar(help, "h", false, "show help")
	if err := root.Parse(args); err != nil {
		return err
	}
	if *help || len(root.Args()) == 0 {
		fmt.Print(usage)
		return nil
	}
	command, commandArgs := root.Args()[0], root.Args()[1:]
	switch command {
	case "create":
		return runCreate(*profileName, commandArgs)
	case "receive", "get":
		return runReceive(*profileName, commandArgs)
	case "delete":
		return runDelete(*profileName, commandArgs)
	case "auth":
		return runAuth(*profileName, commandArgs)
	case "config":
		return runConfig(*profileName, commandArgs)
	case "doctor":
		return runDoctor(*profileName, commandArgs)
	case "schema":
		return runSchema(commandArgs)
	case "skills":
		return runSkills(commandArgs)
	case "version":
		fmt.Println(version)
		return nil
	case "help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q; run `handoff --help`", command)
	}
}

func runCreate(profileName string, args []string) error {
	goalArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	from := flags.String("from", "auto", "context source: auto, codex, claude, or pi")
	mode := flags.String("mode", "agent", "generation mode: agent, local, or server")
	legacyCompact := flags.String("compact", "", "deprecated alias: current, none, or server")
	agentName := flags.String("agent", "auto", "Agent runtime: auto, codex, claude, or pi (never selects a model)")
	var files stringList
	flags.Var(&files, "file", "context file (repeatable)")
	stdin := flags.Bool("stdin", false, "read context from stdin")
	noGit := flags.Bool("no-git", false, "omit repository metadata")
	ttl := flags.Duration("ttl", 7*24*time.Hour, "handoff lifetime")
	jsonOutput := flags.Bool("json", false, "print JSON")
	dryRun := flags.Bool("dry-run", false, "inspect source and request without creating")
	includeTranscript := flags.Bool("include-transcript", false, "authorize retained context upload in server mode (never persisted)")
	review := flags.Bool("review", false, "edit the generated Markdown before publishing")
	output := flags.String("output", "", "also write HANDOFF.md to this path")
	force := flags.Bool("force", false, "overwrite --output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if goalArgument == "" && len(flags.Args()) == 1 {
		goalArgument = flags.Args()[0]
	}
	if strings.TrimSpace(goalArgument) == "" || len(flags.Args()) > 0 {
		return errors.New("usage: handoff create \"next goal\" [--from auto|codex|claude|pi] [--file PATH]")
	}
	setFlags := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { setFlags[item.Name] = true })
	selectedMode, err := resolveCreateMode(*mode, *legacyCompact, setFlags["mode"], setFlags["compact"])
	if err != nil {
		return err
	}
	if selectedMode != "server" && *includeTranscript {
		return errors.New("--include-transcript is only valid with --mode server")
	}
	if selectedMode == "server" && !*includeTranscript && !*dryRun {
		return errors.New("server mode uploads retained source context; repeat with --include-transcript after confirming this is intended")
	}
	goal := card.SanitizeGoal(goalArgument)
	readStdin, stdinReader, err := resolveStdin(*stdin, len(files) > 0)
	if err != nil {
		return err
	}
	contextSource, err := source.Load(source.Options{
		Kind:      *from,
		Files:     files,
		ReadStdin: readStdin,
		Stdin:     stdinReader,
		NoGit:     *noGit,
	})
	if err != nil {
		return err
	}
	contextSource = card.SanitizeContext(contextSource)
	if *dryRun {
		resolvedAgent := ""
		if selectedMode == "agent" {
			resolvedAgent, _ = (agentruntime.Runner{}).Resolve(*agentName, contextSource.Source)
		}
		return printJSON(map[string]any{
			"dry_run":               true,
			"goal":                  goal,
			"source":                contextSource.Source,
			"session_id":            contextSource.SessionID,
			"cursor":                contextSource.Cursor,
			"messages":              len(contextSource.Messages),
			"characters":            contextCharacters(contextSource),
			"repository":            contextSource.Repo,
			"mode":                  selectedMode,
			"agent":                 resolvedAgent,
			"native_compact_found":  contextSource.NativeCompactFound,
			"native_summary_reused": strings.TrimSpace(contextSource.Summary) != "",
			"uploads":               uploadDescription(selectedMode),
			"include_transcript":    *includeTranscript,
			"review":                *review,
			"ttl_seconds":           int64(ttl.Seconds()),
		})
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	apiClient := client.Client{Server: profile.Server, Token: profile.Token}
	sections := card.FallbackSections(goal, contextSource)
	generator := "deterministic"
	var generationWarning error
	switch selectedMode {
	case "agent":
		runner := agentruntime.Runner{}
		runtime, resolveErr := runner.Resolve(*agentName, contextSource.Source)
		if resolveErr != nil {
			generationWarning = resolveErr
		} else if generated, generateErr := runner.Generate(ctx, runtime, goal, contextSource); generateErr != nil {
			generationWarning = generateErr
		} else {
			sections = generated
			generator = "agent:" + runtime
		}
	case "server":
		preview, previewErr := apiClient.PreviewServerCompaction(ctx, types.CompactRequest{
			Goal: goal, Context: contextSource,
		})
		if previewErr != nil {
			return previewErr
		}
		if err := validateServerPreview(preview); err != nil {
			return err
		}
		sections, generator = preview.Sections, preview.Generator
	}
	if *review {
		sections, err = reviewSections(ctx, goal, contextSource, sections, generator, *ttl)
		if err != nil {
			return err
		}
	}
	result, err := apiClient.Publish(ctx, types.PublishRequest{
		Goal: goal,
		Source: types.SourceRef{
			Kind: contextSource.Source, SessionID: contextSource.SessionID,
			Cursor: contextSource.Cursor, UpdatedAt: contextSource.UpdatedAt,
		},
		Sections: sections, Generator: generator, TTLSeconds: int64(ttl.Seconds()),
	})
	if err != nil {
		return err
	}
	if *output != "" {
		if err := writeOutput(*output, result.Handoff.Markdown, *force); err != nil {
			return err
		}
	}
	if *jsonOutput {
		return printJSON(result)
	}
	fmt.Print(formatShareMessage(result))
	if generationWarning != nil {
		fmt.Println("Note:   requested generator was unavailable; used deterministic local extraction")
		fmt.Println("Cause:  " + generationWarning.Error())
	} else if result.Handoff.Generator == "deterministic" {
		fmt.Println("Note:   deterministic local extraction was used")
	}
	if *output != "" {
		absolute, _ := filepath.Abs(*output)
		fmt.Println("Saved:  " + absolute)
	}
	return nil
}

func validateServerPreview(preview types.CompactPreviewResponse) error {
	if preview.Warning != "" {
		return fmt.Errorf("server compaction failed: %s", preview.Warning)
	}
	if !strings.HasPrefix(preview.Generator, "server:") {
		return fmt.Errorf("server compaction failed: unexpected generator %q", preview.Generator)
	}
	return nil
}

func formatShareMessage(result types.CreateResponse) string {
	var message strings.Builder
	message.WriteString("🖐️ **For Human**\n\n")
	if result.ShareURL != "" {
		fmt.Fprintf(&message, "你收到一份 Handoff，请打开[%s](%s)查看。\n", markdownLinkLabel(result.Handoff.Goal), result.ShareURL)
	} else {
		message.WriteString("服务端未返回公开链接，请让发送方检查 Handoff 服务配置。\n")
	}
	message.WriteString("\n🤖 **For Agent**\n\n")
	fmt.Fprintf(&message, "请使用 opengrove-handoff 读取内容，分享码：`%s`\n\n", result.Handoff.ID)
	fmt.Fprintf(&message, "未安装时，请[查看安装方法](%s)。\n\n", installURL)
	fmt.Fprintf(&message, "有效期：%s\n", result.Handoff.ExpiresAt.Local().Format(time.RFC3339))
	return message.String()
}

func markdownLinkLabel(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "Handoff 交接"
	}
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(value)
}

func uploadDescription(mode string) string {
	if mode == "server" {
		return "retained source context for preview, then generated sections for publishing"
	}
	return "generated sections only"
}

func resolveCreateMode(mode, legacyCompact string, modeExplicit, compactExplicit bool) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "agent" && mode != "local" && mode != "server" {
		return "", errors.New("--mode must be agent, local, or server")
	}
	if !compactExplicit {
		return mode, nil
	}
	legacy := map[string]string{"current": "agent", "none": "local", "server": "server"}[strings.ToLower(strings.TrimSpace(legacyCompact))]
	if legacy == "" {
		return "", errors.New("deprecated --compact must be current, none, or server")
	}
	if modeExplicit && mode != legacy {
		return "", fmt.Errorf("--mode %s conflicts with deprecated --compact %s", mode, legacyCompact)
	}
	return legacy, nil
}

func reviewSections(ctx context.Context, goal string, sourceContext types.Context, sections types.Sections, generator string, ttl time.Duration) (types.Sections, error) {
	now := time.Now().UTC()
	draft := types.Handoff{
		Version: types.ProtocolVersion, ID: "review-draft", Goal: goal,
		Source: types.SourceRef{
			Kind: sourceContext.Source, SessionID: sourceContext.SessionID,
			Cursor: sourceContext.Cursor, UpdatedAt: sourceContext.UpdatedAt,
		},
		Generator: generator, CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	file, err := os.CreateTemp("", "handoff-review-*.md")
	if err != nil {
		return types.Sections{}, err
	}
	path := file.Name()
	defer os.Remove(path)
	draftMarkdown := "<!-- Edit the content below, keep the section headings and handoff-section comments, then save and close to publish. -->\n\n" + card.RenderReviewDraft(draft, sections)
	if _, err := io.WriteString(file, draftMarkdown); err != nil {
		file.Close()
		return types.Sections{}, err
	}
	if err := file.Close(); err != nil {
		return types.Sections{}, err
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return types.Sections{}, errors.New("VISUAL or EDITOR is empty")
	}
	command := exec.CommandContext(ctx, parts[0], append(parts[1:], path)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stderr, os.Stderr
	if tty, ttyErr := os.OpenFile("/dev/tty", os.O_RDWR, 0); ttyErr == nil {
		defer tty.Close()
		command.Stdin, command.Stdout = tty, tty
	}
	if err := command.Run(); err != nil {
		return types.Sections{}, fmt.Errorf("review editor failed: %w", err)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return types.Sections{}, err
	}
	parsed, err := card.ParseReviewedMarkdown(string(edited))
	if err != nil {
		return types.Sections{}, fmt.Errorf("reviewed handoff is invalid: %w", err)
	}
	return parsed, nil
}

func runReceive(profileName string, args []string) error {
	idArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff receive", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print JSON")
	output := flags.String("output", "", "write Markdown to a file")
	force := flags.Bool("force", false, "overwrite --output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if idArgument == "" && len(flags.Args()) == 1 {
		idArgument = flags.Args()[0]
	}
	if idArgument == "" || len(flags.Args()) > 0 {
		return errors.New("usage: handoff receive <code-or-url>")
	}
	id, referenceServer := parseHandoffRef(idArgument)
	if id == "" {
		return errors.New("invalid handoff code or URL")
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	if referenceServer != "" {
		profile.Server = referenceServer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := (client.Client{Server: profile.Server, Token: profile.Token}).Get(ctx, id)
	if err != nil {
		return err
	}
	if *output != "" {
		if err := writeOutput(*output, result.Handoff.Markdown, *force); err != nil {
			return err
		}
		absolute, _ := filepath.Abs(*output)
		fmt.Println(absolute)
		return nil
	}
	if *jsonOutput {
		return printJSON(result)
	}
	fmt.Print(result.Handoff.Markdown)
	return nil
}

func runDelete(profileName string, args []string) error {
	idArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff delete", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	yes := flags.Bool("yes", false, "confirm deletion")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if idArgument == "" && len(flags.Args()) == 1 {
		idArgument = flags.Args()[0]
	}
	if idArgument == "" || len(flags.Args()) > 0 {
		return errors.New("usage: handoff delete <code> --yes")
	}
	id, _ := parseHandoffRef(idArgument)
	if id == "" {
		return errors.New("invalid handoff code or URL")
	}
	if !*yes {
		return &structuredError{ExitCode: 10, Payload: map[string]any{
			"ok": false,
			"error": map[string]any{
				"type":    "confirmation_required",
				"subtype": "high_risk_write",
				"message": "deleting a handoff is irreversible",
				"hint":    "run `handoff delete <code> --yes` only after the user explicitly confirms the exact handoff",
			},
			"_meta": map[string]any{"envelope_version": "1.0", "risk": "high-risk-write", "danger": true},
		}}
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := (client.Client{Server: profile.Server, Token: profile.Token}).Delete(ctx, id); err != nil {
		return err
	}
	fmt.Println("Handoff deleted.")
	return nil
}

func runAuth(profileName string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: handoff auth <login|status|logout>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, profile, err := config.Resolve(cfg, profileName)
	if err != nil {
		if profileName == "" {
			return err
		}
		name, profile = profileName, config.Profile{}
	}
	switch args[0] {
	case "login":
		flags := flag.NewFlagSet("handoff auth login", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		server := flags.String("server", profile.Server, "handoffd base URL")
		tokenStdin := flags.Bool("token-stdin", false, "read API token from stdin")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		token := strings.TrimSpace(os.Getenv("HANDOFF_TOKEN"))
		if *tokenStdin {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<10))
			if err != nil {
				return err
			}
			token = strings.TrimSpace(string(data))
		}
		if strings.TrimSpace(*server) == "" || token == "" {
			return errors.New("server and token are required; use --token-stdin or HANDOFF_TOKEN")
		}
		if cfg.Profiles == nil {
			cfg.Profiles = map[string]config.Profile{}
		}
		cfg.Profiles[name] = config.Profile{Server: strings.TrimRight(*server, "/"), Token: token}
		cfg.Current = name
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Logged in to %s as profile %q.\n", strings.TrimRight(*server, "/"), name)
		return nil
	case "status":
		return printJSON(map[string]any{"profile": name, "server": profile.Server, "authenticated": profile.Token != ""})
	case "logout":
		profile.Token = ""
		cfg.Profiles[name] = profile
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Logged out profile %q.\n", name)
		return nil
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func runConfig(profileName string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: handoff config <show|set-server>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name, profile, err := config.Resolve(cfg, profileName)
	if err != nil {
		return err
	}
	switch args[0] {
	case "show":
		path, _ := config.Path()
		return printJSON(map[string]any{"profile": name, "server": profile.Server, "authenticated": profile.Token != "", "path": path})
	case "set-server":
		if len(args) != 2 {
			return errors.New("usage: handoff config set-server <url>")
		}
		if _, err := url.ParseRequestURI(args[1]); err != nil {
			return fmt.Errorf("invalid server URL: %w", err)
		}
		profile.Server = strings.TrimRight(args[1], "/")
		cfg.Profiles[name] = profile
		cfg.Current = name
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Println(profile.Server)
		return nil
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func runDoctor(profileName string, args []string) error {
	flags := flag.NewFlagSet("handoff doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	offline := flags.Bool("offline", false, "skip server connectivity")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	name, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	checks := []map[string]any{
		{"check": "profile", "ok": true, "detail": name},
		{"check": "auth", "ok": profile.Token != "", "detail": boolDetail(profile.Token != "", "token configured", "token missing")},
	}
	contextSource, sourceErr := source.Load(source.Options{Kind: "auto", NoGit: true})
	if sourceErr != nil {
		checks = append(checks, map[string]any{"check": "source", "ok": false, "detail": sourceErr.Error()})
	} else {
		checks = append(checks, map[string]any{"check": "source", "ok": true, "detail": "workspace session detected"})
		runtime, agentErr := (agentruntime.Runner{}).Resolve("auto", contextSource.Source)
		if agentErr != nil {
			checks = append(checks, map[string]any{"check": "current_agent", "ok": false, "detail": agentErr.Error()})
		} else {
			checks = append(checks, map[string]any{"check": "current_agent", "ok": true, "detail": runtime + " (existing auth/config/default model)"})
		}
	}
	failed := profile.Token == ""
	if !*offline {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, healthErr := (client.Client{Server: profile.Server, Token: profile.Token}).Health(ctx)
		cancel()
		if healthErr != nil {
			checks = append(checks, map[string]any{"check": "server", "ok": false, "detail": healthErr.Error()})
			failed = true
		} else {
			checks = append(checks, map[string]any{"check": "server", "ok": true, "detail": profile.Server})
		}
	}
	if err := printJSON(map[string]any{"ok": !failed, "checks": checks}); err != nil {
		return err
	}
	if failed {
		return errors.New("doctor found blocking problems")
	}
	return nil
}

func runSchema(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: handoff schema [create|receive|delete]")
	}
	if len(args) == 0 {
		return printJSON(map[string]any{
			"ok":       true,
			"commands": []string{"create", "receive", "delete"},
			"hint":     "run `handoff schema <command>` for its JSON Schema contract",
		})
	}
	contract, err := schemaContract(args[0])
	if err != nil {
		return err
	}
	return printJSON(contract)
}

func schemaContract(command string) (map[string]any, error) {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	booleanProperty := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	meta := func(risk string) map[string]any {
		return map[string]any{"envelope_version": "1.0", "risk": risk, "danger": risk == "high-risk-write"}
	}
	switch command {
	case "create":
		return map[string]any{
			"name":        "handoff create",
			"description": "Generate and publish an immutable handoff from a read-only context snapshot.",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"goal"}, "additionalProperties": false,
				"properties": map[string]any{
					"goal":               stringProperty("The next concrete goal for the receiver."),
					"from":               map[string]any{"type": "string", "enum": []string{"auto", "codex", "claude", "pi"}, "default": "auto"},
					"mode":               map[string]any{"type": "string", "enum": []string{"agent", "local", "server"}, "default": "agent", "description": "agent and local publish sections only; server also requires include_transcript."},
					"agent":              map[string]any{"type": "string", "enum": []string{"auto", "codex", "claude", "pi"}, "default": "auto", "description": "Selects an Agent runtime, never a model."},
					"include_transcript": booleanProperty("Explicitly authorize retained context upload; valid only with server mode."),
					"review":             booleanProperty("Edit generated Markdown before publishing."),
					"file":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repeatable context file path."},
					"stdin":              booleanProperty("Read context from stdin."),
					"no_git":             booleanProperty("Omit repository metadata."),
					"ttl":                map[string]any{"type": "string", "default": "168h", "description": "Go duration for the handoff lifetime."},
					"json":               booleanProperty("Print machine-readable output."),
					"dry_run":            booleanProperty("Inspect source and upload behavior without an Agent or network write."),
					"output":             stringProperty("Also write HANDOFF.md to this path."),
					"force":              booleanProperty("Allow overwriting the output file."),
				},
			},
			"outputSchema": createOutputSchema(),
			"_meta": map[string]any{
				"envelope_version": "1.0", "risk": "write", "danger": false,
				"session":              "read-only snapshot; never compacted, resumed, or modified",
				"default_upload":       "generated sections only",
				"default_model_config": "inherits the current Agent runtime",
				"native_compact":       "reuses a readable native summary plus its retained tail; never invokes native /compact",
			},
		}, nil
	case "receive":
		return map[string]any{
			"name": "handoff receive", "description": "Fetch a handoff by capability code or URL.",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"code_or_url"}, "additionalProperties": false,
				"properties": map[string]any{
					"code_or_url": stringProperty("Branded opengrove-handoff reference, share code, human URL, or raw Markdown URL."),
					"json":        booleanProperty("Print machine-readable output."),
					"output":      stringProperty("Write Markdown to this path."),
					"force":       booleanProperty("Allow overwriting the output file."),
				},
			},
			"outputSchema": createOutputSchema(), "_meta": meta("read"),
		}, nil
	case "delete":
		return map[string]any{
			"name": "handoff delete", "description": "Permanently delete a handoff before expiry.",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"code_or_url", "yes"}, "additionalProperties": false,
				"properties": map[string]any{
					"code_or_url": stringProperty("Exact handoff share code or URL to delete."),
					"yes":         map[string]any{"type": "boolean", "const": true, "description": "Explicit confirmation after user approval."},
				},
			},
			"outputSchema": map[string]any{"type": "object", "required": []string{"deleted"}, "properties": map[string]any{"deleted": map[string]any{"type": "boolean", "const": true}}},
			"_meta":        meta("high-risk-write"),
		}, nil
	default:
		return nil, fmt.Errorf("unknown schema %q; expected create, receive, or delete", command)
	}
}

func createOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"handoff"},
		"properties": map[string]any{
			"handoff": map[string]any{
				"type": "object", "required": []string{"version", "id", "goal", "markdown", "generator", "created_at", "expires_at"},
				"properties": map[string]any{
					"version":    map[string]any{"type": "integer"},
					"id":         map[string]any{"type": "string"},
					"goal":       map[string]any{"type": "string"},
					"markdown":   map[string]any{"type": "string"},
					"generator":  map[string]any{"type": "string"},
					"created_at": map[string]any{"type": "string", "format": "date-time"},
					"expires_at": map[string]any{"type": "string", "format": "date-time"},
				},
			},
			"share_url":    map[string]any{"type": "string", "format": "uri"},
			"markdown_url": map[string]any{"type": "string", "format": "uri"},
		},
	}
}

const skillsUsage = `Read Agent Skills embedded in the handoff CLI so instructions stay in sync with the binary.

Usage:
  handoff skills list [--json]
  handoff skills read <name> [--json]
`

func runSkills(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(skillsUsage)
		return nil
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("handoff skills list", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		jsonOutput := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if len(flags.Args()) != 0 {
			return errors.New("usage: handoff skills list [--json]")
		}
		available := skillbundle.List()
		if *jsonOutput {
			return printJSON(map[string]any{"ok": true, "skills": available, "count": len(available)})
		}
		for _, skill := range available {
			fmt.Printf("%s\t%s\t%s\n", skill.Name, skill.Version, skill.Description)
		}
		return nil
	case "read":
		name, rest := leadingArgument(args[1:])
		flags := flag.NewFlagSet("handoff skills read", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		jsonOutput := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if name == "" && len(flags.Args()) == 1 {
			name = flags.Args()[0]
		}
		if name == "" || len(flags.Args()) > 0 {
			return errors.New("usage: handoff skills read <name> [--json]")
		}
		content, ok := skillbundle.Read(name)
		if !ok {
			return fmt.Errorf("unknown skill %q; run `handoff skills list`", name)
		}
		if *jsonOutput {
			return printJSON(map[string]any{"ok": true, "name": name, "content": content})
		}
		fmt.Print(content)
		return nil
	default:
		return fmt.Errorf("unknown skills command %q; run `handoff skills --help`", args[0])
	}
}

func loadProfile(name string) (string, config.Profile, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", config.Profile{}, err
	}
	return config.Resolve(cfg, name)
}

func resolveStdin(explicit, hasFiles bool) (bool, io.Reader, error) {
	if hasFiles {
		return false, nil, nil
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return false, nil, err
	}
	if !explicit && info.Mode()&os.ModeCharDevice != 0 {
		return false, nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		return false, nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if explicit {
			return false, nil, errors.New("no context received on stdin")
		}
		return false, nil, nil
	}
	return true, bytes.NewReader(data), nil
}

func contextCharacters(input types.Context) int {
	total := len(input.Summary)
	for _, message := range input.Messages {
		total += len(message.Text)
	}
	return total
}

func parseHandoffRef(value string) (string, string) {
	value = strings.TrimSpace(value)
	if match := brandedHandoffRef.FindStringSubmatch(value); len(match) == 2 {
		return match[1], ""
	}
	if embedded := embeddedHandoffURL.FindString(value); embedded != "" {
		value = embedded
	}
	server := ""
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		server = parsed.Scheme + "://" + parsed.Host
		value = strings.Trim(parsed.Path, "/")
		if slash := strings.LastIndex(value, "/"); slash >= 0 {
			value = value[slash+1:]
		}
		value = strings.TrimSuffix(value, ".md")
	}
	if len(value) < 20 || len(value) > 32 {
		return "", ""
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return "", ""
		}
	}
	return value, server
}

func writeOutput(path, value string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("output exists: %s (use --force to overwrite)", path)
		}
		return err
	}
	if _, err := io.WriteString(file, value); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func printJSON(value any) error {
	return writeJSON(os.Stdout, value)
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func boolDetail(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}

func leadingArgument(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
