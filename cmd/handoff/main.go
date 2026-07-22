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
	"path/filepath"
	"strings"
	"time"

	agentruntime "github.com/open-grove/handoff/internal/agent"
	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/client"
	"github.com/open-grove/handoff/internal/config"
	"github.com/open-grove/handoff/internal/source"
	"github.com/open-grove/handoff/internal/types"
)

const version = "0.2.1"

const usage = `handoff — portable context for people and agents.

AGENT QUICKSTART:
  handoff create "next goal"                 Auto-detect this workspace's latest Agent session
  agent-export | handoff create "next goal"  Create from stdin
  handoff receive <code>                     Print a received HANDOFF.md
  handoff schema create                      Inspect the create contract

Usage:
  handoff [--profile NAME] <command> [flags]

Commands:
  create       Compact with the current Agent and share a handoff     Risk: write
  receive      Fetch a handoff by code or URL                         Risk: read
  delete       Delete a handoff before it expires                     Risk: high-risk-write
  auth         Login, status, and logout
  config       Show or update CLI configuration
  doctor       Check local source detection, auth, and connectivity   Risk: read
  schema       Print machine-readable command contracts               Risk: read
  version      Print the CLI version

Global flags:
  --profile NAME   Use a named configuration profile
  -h, --help       Show help

Context sources:
  auto (default), codex, claude, pi, piped stdin, or one or more --file values.

Compaction:
  current (default) reuses the current Agent's auth, config, and default model.
  It starts an ephemeral sidecar and never compacts or resumes the source session.
`

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
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
	compactMode := flags.String("compact", "current", "compaction: current, none, or server")
	agentName := flags.String("agent", "auto", "Agent runtime: auto, codex, claude, or pi (never selects a model)")
	var files stringList
	flags.Var(&files, "file", "context file (repeatable)")
	stdin := flags.Bool("stdin", false, "read context from stdin")
	noGit := flags.Bool("no-git", false, "omit repository metadata")
	ttl := flags.Duration("ttl", 7*24*time.Hour, "handoff lifetime")
	jsonOutput := flags.Bool("json", false, "print JSON")
	dryRun := flags.Bool("dry-run", false, "inspect source and request without creating")
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
	if *compactMode != "current" && *compactMode != "none" && *compactMode != "server" {
		return errors.New("--compact must be current, none, or server")
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
		if *compactMode == "current" {
			resolvedAgent, _ = (agentruntime.Runner{}).Resolve(*agentName, contextSource.Source)
		}
		return printJSON(map[string]any{
			"dry_run":     true,
			"goal":        goal,
			"source":      contextSource.Source,
			"session_id":  contextSource.SessionID,
			"cursor":      contextSource.Cursor,
			"messages":    len(contextSource.Messages),
			"characters":  contextCharacters(contextSource),
			"repository":  contextSource.Repo,
			"compact":     *compactMode,
			"agent":       resolvedAgent,
			"uploads":     uploadDescription(*compactMode),
			"ttl_seconds": int64(ttl.Seconds()),
		})
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	apiClient := client.Client{Server: profile.Server, Token: profile.Token}
	var result types.CreateResponse
	var compactWarning error
	if *compactMode == "server" {
		result, err = apiClient.CompactOnServer(ctx, types.CompactRequest{
			Goal: goal, Context: contextSource, TTLSeconds: int64(ttl.Seconds()),
		})
	} else {
		sections := card.FallbackSections(goal, contextSource)
		generator := "deterministic"
		if *compactMode == "current" {
			runner := agentruntime.Runner{}
			runtime, resolveErr := runner.Resolve(*agentName, contextSource.Source)
			if resolveErr != nil {
				compactWarning = resolveErr
			} else if compacted, compactErr := runner.Compact(ctx, runtime, goal, contextSource); compactErr != nil {
				compactWarning = compactErr
			} else {
				sections = compacted
				generator = "agent:" + runtime
			}
		}
		result, err = apiClient.Publish(ctx, types.PublishRequest{
			Goal: goal,
			Source: types.SourceRef{
				Kind: contextSource.Source, SessionID: contextSource.SessionID,
				Cursor: contextSource.Cursor, UpdatedAt: contextSource.UpdatedAt,
			},
			Sections: sections, Generator: generator, TTLSeconds: int64(ttl.Seconds()),
		})
	}
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
	fmt.Println("Handoff ready")
	fmt.Println("Code:   " + result.Handoff.ID)
	if result.ShareURL != "" {
		fmt.Println("Open:   " + result.ShareURL)
	}
	fmt.Println("Get:    handoff receive " + result.Handoff.ID)
	fmt.Println("Expiry: " + result.Handoff.ExpiresAt.Local().Format(time.RFC3339))
	if compactWarning != nil {
		fmt.Println("Note:   current Agent was unavailable; used deterministic local compaction")
		fmt.Println("Cause:  " + compactWarning.Error())
	} else if result.Handoff.Generator == "deterministic" {
		fmt.Println("Note:   deterministic compaction was used")
	}
	if *output != "" {
		absolute, _ := filepath.Abs(*output)
		fmt.Println("Saved:  " + absolute)
	}
	return nil
}

func uploadDescription(compactMode string) string {
	if compactMode == "server" {
		return "retained source context"
	}
	return "compacted sections only"
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
	if idArgument == "" || len(flags.Args()) > 0 || !*yes {
		return errors.New("usage: handoff delete <code> --yes")
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, _ := parseHandoffRef(idArgument)
	if id == "" {
		return errors.New("invalid handoff code or URL")
	}
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
	if len(args) != 1 || args[0] != "create" {
		return errors.New("usage: handoff schema create")
	}
	return printJSON(map[string]any{
		"command": "handoff create \"next goal\"",
		"risk":    "write",
		"input": map[string]any{
			"goal":    "required positional string",
			"from":    []string{"auto", "codex", "claude", "pi", "stdin", "file"},
			"compact": []string{"current (default)", "none", "server (explicit raw-context upload)"},
			"agent":   []string{"auto", "codex", "claude", "pi"},
			"ttl":     "duration; default 168h",
		},
		"model_config": "none; current mode inherits the Agent runtime's auth, config, and default model",
		"session":      "read-only snapshot; source session is never compacted, resumed, or modified",
		"upload":       "compacted sections only, unless --compact server is explicitly selected",
		"output":       "versioned handoff JSON plus portable Markdown",
		"dry_run":      "use --dry-run to inspect metadata without an Agent call or network write",
	})
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
	server := ""
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		server = parsed.Scheme + "://" + parsed.Host
		value = strings.Trim(parsed.Path, "/")
		if slash := strings.LastIndex(value, "/"); slash >= 0 {
			value = value[slash+1:]
		}
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
	encoder := json.NewEncoder(os.Stdout)
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
