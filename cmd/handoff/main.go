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
	"strconv"
	"strings"
	"time"

	agentruntime "github.com/open-grove/handoff/internal/agent"
	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/client"
	"github.com/open-grove/handoff/internal/config"
	"github.com/open-grove/handoff/internal/opengroveauth"
	"github.com/open-grove/handoff/internal/ownership"
	"github.com/open-grove/handoff/internal/source"
	"github.com/open-grove/handoff/internal/types"
	"github.com/open-grove/handoff/internal/updater"
	skillbundle "github.com/open-grove/handoff/skills"
)

const version = skillbundle.Version

const installURL = "https://github.com/open-grove/handoff"
const maxServerRequestBytes = 4 << 20

var brandedHandoffRef = regexp.MustCompile("(?i)opengrove-handoff\\s*(?:读取内容\\s*)?[,，]?\\s*(?:分享码|code)\\s*[:：]\\s*`?([A-Za-z0-9_-]{20,32})")
var canonicalHandoffRef = regexp.MustCompile(`(?i)opengrove-handoff:(?://)?([A-Za-z0-9_-]{20,32})`)
var embeddedHandoffURL = regexp.MustCompile(`https://[A-Za-z0-9.-]+(?::[0-9]+)?/h/[A-Za-z0-9_-]{20,32}(?:\.md)?`)

const usage = `handoff — portable context for people and agents.

AGENT QUICKSTART:
  handoff create "next goal"                 Use this workspace's active Agent session when available
  agent-export | handoff create "next goal"  Create from stdin
  handoff create "next goal" --generator cloud
  handoff create "next goal" --attach-context
  handoff session locate                     Return the same-machine Session path
  handoff receive opengrove-handoff:<code>   Print a received HANDOFF.md
  handoff context opengrove-handoff:<code>   Read an attached full Context
  handoff whoami                             Show identity and cloud-generation access
  handoff schema create                      Inspect the create contract

Usage:
  handoff [--profile NAME] <command> [flags]

Commands:
  create       Generate and publish a handoff                         Risk: write
  session      Locate a same-machine provider Session                 Risk: read
  receive      Fetch a handoff by reference, code, or URL             Risk: read
  context      Fetch an explicitly attached full readable Context     Risk: read
  delete       Delete a handoff before it expires                     Risk: high-risk-write
  admin        Manage the optional service administrator credential  Risk: write
  config       Show or update CLI configuration                       Risk: read/write
  doctor       Check discovery, identity, and connectivity            Risk: read
  whoami       Show OpenGrove identity and cloud access               Risk: read
  update       Install a verified release and matching Skill          Risk: high-risk-write
  schema       Print exact machine-readable command contracts         Risk: read
  skills       Inspect or repair embedded Agent Skills                Risk: read/write
  version      Print the CLI version                                  Risk: read

Global flags:
  --profile NAME   Use a named configuration profile
  --json           Machine-readable JSON output
  --format FORMAT  Output format: text or json
  -h, --help       Show help

Context sources:
  auto (default), codex, claude, pi, piped stdin, or one or more --file values.

Generators:
  agent (default) reuses the current Agent's auth, config, and default model.
  deterministic performs local extraction. cloud requires OpenGrove login.
  --attach-context independently stores the full sanitized readable context.
  The source session is always read-only and is never compacted or resumed.
`

const createUsage = `Generate and publish an immutable handoff.

Usage:
  handoff create "next goal" [flags]

Preferred flags:
  --generator agent|deterministic|cloud   Generation strategy (default: agent)
  --source auto|codex|claude|pi           Context source (default: auto)
  --runtime auto|codex|claude|pi          Agent runtime; never selects a model
  --attach-context                        Store full sanitized readable context
  --file PATH                             Context file; repeatable
  --review                                Edit generated Markdown before publish
  --dry-run                               Inspect without Agent or network write
  --output PATH                           Also write HANDOFF.md
  --force                                 Overwrite --output
  --no-git                                Omit repository metadata
  --ttl DURATION                          Lifetime (default: 168h)

Risk: write

Compatibility:
  --upload-context, --mode, --from, --agent, --include-transcript,
  --full-session, --stdin, and --compact remain accepted temporarily.
`

const sessionUsage = `Locate a provider Session file for same-machine use.

Usage:
  handoff session locate [--source auto|codex|claude|pi] [--goal TEXT]

The raw provider Session is not uploaded or redacted. Do not share its path or
contents in public or cross-device channels.

Risk: read
`

const adminUsage = `Manage the optional Handoff service administrator credential.

Usage:
  handoff admin login [--server URL] [--token-stdin]
  handoff admin status
  handoff admin logout

This is not OpenGrove user login. Use handoff whoami for cloud-generation access.

Risk: write
`

const configUsage = `Show or update Handoff CLI configuration.

Usage:
  handoff config show
  handoff config set-server <https-url>

Risk: read/write
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
		if activeOutputFormat == "json" {
			_ = writeJSON(os.Stderr, map[string]any{
				"ok": false,
				"error": map[string]any{
					"type":    "command_failed",
					"message": err.Error(),
				},
				"_meta": map[string]any{"envelope_version": "1.0"},
			})
			os.Exit(1)
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

var (
	pendingNotices     map[string]any
	activeOutputFormat string
)

func run(args []string) error {
	pendingNotices = nil
	args, outputFormat, err := extractOutputFormat(args)
	if err != nil {
		return err
	}
	activeOutputFormat = outputFormat
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
	if outputFormat == "json" && command != "update" && command != "schema" && command != "skills" && command != "version" && command != "help" {
		maybeAddUpdateNotice()
	}
	switch command {
	case "create":
		return runCreate(*profileName, outputFormat, commandArgs)
	case "session":
		return runSession(*profileName, outputFormat, commandArgs)
	case "receive":
		return runReceive(*profileName, outputFormat, commandArgs)
	case "context":
		return runContext(*profileName, outputFormat, commandArgs)
	case "get":
		addDeprecationNotice("`handoff get` is deprecated; use `handoff receive`")
		return runReceive(*profileName, outputFormat, commandArgs)
	case "delete":
		return runDelete(*profileName, outputFormat, commandArgs)
	case "admin":
		return runAdmin(*profileName, outputFormat, commandArgs)
	case "auth":
		addDeprecationNotice("`handoff auth` is deprecated; use `handoff admin`")
		return runAdmin(*profileName, outputFormat, commandArgs)
	case "config":
		return runConfig(*profileName, outputFormat, commandArgs)
	case "doctor":
		return runDoctor(*profileName, outputFormat, commandArgs)
	case "whoami":
		return runWhoAmI(*profileName, outputFormat, commandArgs)
	case "update":
		return runUpdate(outputFormat, commandArgs)
	case "schema":
		return runSchema(outputFormat, commandArgs)
	case "skills":
		return runSkills(outputFormat, commandArgs)
	case "version":
		if wantsHelp(commandArgs) {
			fmt.Println("Usage: handoff version\n\nRisk: read")
			return nil
		}
		if len(commandArgs) != 0 {
			return errors.New("usage: handoff version")
		}
		if outputFormat == "json" {
			return printJSON(map[string]any{"version": version})
		}
		fmt.Println(version)
		return nil
	case "help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q; run `handoff --help`", command)
	}
}

func runCreate(profileName, outputFormat string, args []string) error {
	goalArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stdout, createUsage) }
	sourceName := flags.String("source", "auto", "context source: auto, codex, claude, or pi")
	generatorName := flags.String("generator", "agent", "generator: agent, deterministic, or cloud")
	runtimeName := flags.String("runtime", "auto", "Agent runtime: auto, codex, claude, or pi (never selects a model)")
	attachContext := flags.Bool("attach-context", false, "store full sanitized readable context beside the handoff")
	uploadContext := flags.String("upload-context", "", "deprecated cloud upload acknowledgement: selected or full")
	legacyFrom := flags.String("from", "auto", "deprecated alias for --source")
	legacyMode := flags.String("mode", "agent", "deprecated generator alias: agent, local, server, or session")
	legacyCompact := flags.String("compact", "", "deprecated alias: current, none, or server")
	legacyAgent := flags.String("agent", "auto", "deprecated alias for --runtime")
	var files stringList
	flags.Var(&files, "file", "context file (repeatable)")
	legacyStdin := flags.Bool("stdin", false, "deprecated: pipe context through stdin instead")
	noGit := flags.Bool("no-git", false, "omit repository metadata")
	ttl := flags.Duration("ttl", 7*24*time.Hour, "handoff lifetime")
	jsonOutput := flags.Bool("json", false, "print JSON")
	dryRun := flags.Bool("dry-run", false, "inspect source and request without creating")
	legacyIncludeTranscript := flags.Bool("include-transcript", false, "deprecated cloud upload authorization")
	legacyFullSession := flags.Bool("full-session", false, "deprecated alias for --upload-context full")
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
		return errors.New("usage: handoff create \"next goal\" [--source auto|codex|claude|pi] [--generator agent|deterministic|cloud]")
	}
	setFlags := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { setFlags[item.Name] = true })
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: *sourceName, Generator: *generatorName, Runtime: *runtimeName, UploadContext: *uploadContext,
		AttachContext: *attachContext,
		LegacyFrom:    *legacyFrom, LegacyMode: *legacyMode, LegacyCompact: *legacyCompact, LegacyAgent: *legacyAgent,
		LegacyIncludeTranscript: *legacyIncludeTranscript, LegacyFullSession: *legacyFullSession,
		Set: setFlags,
	})
	if err != nil {
		return err
	}
	for _, deprecated := range selection.Deprecated {
		addDeprecationNotice(deprecated)
	}
	if selection.Generator != "cloud" && selection.UploadContext != "" {
		return errors.New("deprecated --upload-context is only valid with --generator cloud; use --attach-context to persist Context independently")
	}
	if selection.SessionPath && (len(files) > 0 || *legacyStdin) {
		return errors.New("session mode resolves an Agent session file and cannot be combined with --file or --stdin")
	}
	if selection.SessionPath && (*review || *output != "" || *force) {
		return errors.New("session mode only prints a local path and cannot be combined with --review, --output, or --force")
	}
	goal := card.SanitizeGoal(goalArgument)
	var readStdin bool
	var stdinReader io.Reader
	if !selection.SessionPath {
		readStdin, stdinReader, err = resolveStdin(*legacyStdin, len(files) > 0)
		if err != nil {
			return err
		}
	}
	contextSource, err := source.Load(source.Options{
		Kind:      selection.Source,
		Files:     files,
		ReadStdin: readStdin,
		Stdin:     stdinReader,
		NoGit:     *noGit || selection.SessionPath,
	})
	if err != nil {
		return err
	}
	if selection.SessionPath {
		return printLocalSession(goal, contextSource, outputFormat == "json" || *jsonOutput, *dryRun)
	}
	contextSource = card.SanitizeContext(contextSource)
	var contextAttachment *types.ContextAttachment
	if selection.AttachContext {
		attachment := card.BuildContextAttachment(contextSource)
		contextAttachment = &attachment
	}
	attachmentBytes := 0
	if contextAttachment != nil {
		encoded, encodeErr := json.Marshal(contextAttachment)
		if encodeErr != nil {
			return encodeErr
		}
		attachmentBytes = len(encoded)
	}
	serverRequestBytes := 0
	if selection.Generator == "cloud" {
		encoded, encodeErr := json.Marshal(types.CompactRequest{Goal: goal, Context: contextSource})
		if encodeErr != nil {
			return encodeErr
		}
		serverRequestBytes = len(encoded)
		if !*dryRun && serverRequestBytes > maxServerRequestBytes {
			return fmt.Errorf(
				"sanitized Session request is %.1f MiB; the server limit is %.1f MiB (use `handoff session locate` for same-machine access)",
				float64(serverRequestBytes)/(1<<20),
				float64(maxServerRequestBytes)/(1<<20),
			)
		}
	}
	if *dryRun {
		resolvedAgent := ""
		if selection.Generator == "agent" {
			resolvedAgent, _ = (agentruntime.Runner{}).Resolve(selection.Runtime, contextSource.Source)
		}
		return printJSON(map[string]any{
			"dry_run":                  true,
			"goal":                     goal,
			"source":                   contextSource.Source,
			"session_id":               contextSource.SessionID,
			"cursor":                   contextSource.Cursor,
			"messages":                 len(contextSource.Messages),
			"characters":               contextCharacters(contextSource),
			"repository":               contextSource.Repo,
			"generator":                selection.Generator,
			"runtime":                  resolvedAgent,
			"native_compact_found":     contextSource.NativeCompactFound,
			"native_summary_auxiliary": strings.TrimSpace(contextSource.Summary) != "",
			"attach_context":           selection.AttachContext,
			"attachment_bytes":         attachmentBytes,
			"uploads":                  uploadDescription(selection.Generator, selection.AttachContext),
			"request_bytes":            serverRequestBytes,
			"review":                   *review,
			"ttl_seconds":              int64(ttl.Seconds()),
		})
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	commandTimeout := 6 * time.Minute
	if selection.Generator == "cloud" {
		commandTimeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	apiClient := client.Client{Server: profile.Server}
	sections := card.FallbackSections(goal, contextSource)
	generator := "deterministic"
	var generationWarning error
	switch selection.Generator {
	case "agent":
		runner := agentruntime.Runner{}
		runtime, resolveErr := runner.Resolve(selection.Runtime, contextSource.Source)
		if resolveErr != nil {
			generationWarning = resolveErr
		} else if generated, generateErr := runner.Generate(ctx, runtime, goal, contextSource); generateErr != nil {
			generationWarning = generateErr
		} else {
			sections = generated
			generator = "agent:" + runtime
		}
	case "cloud":
		accessToken, authErr := opengroveauth.AccessToken(time.Now())
		if authErr != nil {
			return authErr
		}
		compactionClient := client.Client{Server: profile.Server, Token: accessToken}
		preview, previewErr := compactionClient.PreviewServerCompaction(ctx, types.CompactRequest{
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
	publishRequest := types.PublishRequest{
		Goal: goal,
		Source: types.SourceRef{
			Kind: contextSource.Source, UpdatedAt: contextSource.UpdatedAt,
		},
		Sections: sections, Generator: generator, ContextAttachment: contextAttachment,
		TTLSeconds: int64(ttl.Seconds()),
	}
	publishBody, encodeErr := json.Marshal(publishRequest)
	if encodeErr != nil {
		return encodeErr
	}
	if len(publishBody) > maxServerRequestBytes {
		return fmt.Errorf(
			"published handoff is %.1f MiB; the server limit is %.1f MiB (omit --attach-context or reduce the source)",
			float64(len(publishBody))/(1<<20),
			float64(maxServerRequestBytes)/(1<<20),
		)
	}
	result, err := apiClient.Publish(ctx, publishRequest)
	if err != nil {
		return err
	}
	deleteCredentialSaved := false
	var deleteCredentialWarning error
	if result.DeleteToken != "" {
		if saveErr := ownership.Save(profile.Server, result.Handoff.ID, result.DeleteToken); saveErr != nil {
			deleteCredentialWarning = saveErr
		} else {
			deleteCredentialSaved = true
		}
		result.DeleteToken = ""
	}
	if *output != "" {
		if err := writeOutput(*output, result.Handoff.Markdown, *force); err != nil {
			return err
		}
	}
	if outputFormat == "json" || *jsonOutput {
		return printJSON(createCommandOutput(result, deleteCredentialSaved, generationWarning))
	}
	fmt.Print(formatShareMessage(result))
	if deleteCredentialWarning != nil {
		fmt.Println("Warning: the private delete credential could not be saved locally; this handoff may require an administrator to delete")
	} else if deleteCredentialSaved {
		fmt.Println("Delete: private credential saved locally")
	}
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
		return fmt.Errorf("cloud generation failed: %s", preview.Warning)
	}
	if !strings.HasPrefix(preview.Generator, "server:") {
		return fmt.Errorf("cloud generation failed: unexpected generator %q", preview.Generator)
	}
	return nil
}

func formatShareMessage(result types.CreateResponse) string {
	var message strings.Builder
	message.WriteString("🖐️ **For Human**\n\n")
	if result.ShareURL != "" {
		title := result.Handoff.Title
		if strings.TrimSpace(title) == "" {
			title = card.CompactTitle(result.Handoff.Goal)
		}
		fmt.Fprintf(&message, "你收到一份 Handoff，请打开[%s](%s)查看。\n", markdownLinkLabel(title), result.ShareURL)
	} else {
		message.WriteString("服务端未返回公开链接，请让发送方检查 Handoff 服务配置。\n")
	}
	message.WriteString("\n🤖 **For Agent**\n\n")
	fmt.Fprintf(&message, "请使用 OpenGrove Handoff 读取：`%s`\n\n", agentReference(result.Handoff.ID))
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

func uploadDescription(generator string, attachContext bool) string {
	if generator == "cloud" {
		if attachContext {
			return "canonical sanitized readable context for transient cloud generation, then generated sections plus the explicit context attachment for publishing"
		}
		return "canonical sanitized readable context for transient cloud generation, then generated sections only for publishing"
	}
	if attachContext {
		return "generated sections plus the explicit full sanitized readable context attachment"
	}
	return "generated sections only"
}

type createSelectionInput struct {
	Source, Generator, Runtime, UploadContext          string
	LegacyFrom, LegacyMode, LegacyCompact, LegacyAgent string
	LegacyIncludeTranscript, LegacyFullSession         bool
	AttachContext                                      bool
	Set                                                map[string]bool
}

type createSelection struct {
	Source, Generator, Runtime, UploadContext string
	AttachContext, SessionPath                bool
	Deprecated                                []string
}

func resolveCreateSelection(input createSelectionInput) (createSelection, error) {
	selection := createSelection{
		Source:        strings.ToLower(strings.TrimSpace(input.Source)),
		Generator:     strings.ToLower(strings.TrimSpace(input.Generator)),
		Runtime:       strings.ToLower(strings.TrimSpace(input.Runtime)),
		UploadContext: strings.ToLower(strings.TrimSpace(input.UploadContext)),
		AttachContext: input.AttachContext,
	}
	validRuntime := func(value string) bool {
		return value == "auto" || value == "codex" || value == "claude" || value == "pi"
	}
	if !validRuntime(selection.Source) {
		return createSelection{}, errors.New("--source must be auto, codex, claude, or pi")
	}
	if !validRuntime(selection.Runtime) {
		return createSelection{}, errors.New("--runtime must be auto, codex, claude, or pi")
	}
	if selection.Generator != "agent" && selection.Generator != "deterministic" && selection.Generator != "cloud" {
		return createSelection{}, errors.New("--generator must be agent, deterministic, or cloud")
	}
	if selection.UploadContext != "" && selection.UploadContext != "selected" && selection.UploadContext != "full" {
		return createSelection{}, errors.New("--upload-context must be selected or full")
	}
	applyAlias := func(canonicalName, canonicalValue, legacyName, legacyValue string, mapValue func(string) string) error {
		if !input.Set[legacyName] {
			return nil
		}
		mapped := mapValue(strings.ToLower(strings.TrimSpace(legacyValue)))
		if mapped == "" {
			return fmt.Errorf("deprecated --%s has an invalid value %q", legacyName, legacyValue)
		}
		if input.Set[canonicalName] && canonicalValue != mapped {
			return fmt.Errorf("--%s %s conflicts with deprecated --%s %s", canonicalName, canonicalValue, legacyName, legacyValue)
		}
		selection.Deprecated = append(selection.Deprecated, "--"+legacyName+" is deprecated")
		switch canonicalName {
		case "source":
			selection.Source = mapped
		case "runtime":
			selection.Runtime = mapped
		case "generator":
			selection.Generator = mapped
		}
		return nil
	}
	identity := func(value string) string {
		if validRuntime(value) {
			return value
		}
		return ""
	}
	if err := applyAlias("source", selection.Source, "from", input.LegacyFrom, identity); err != nil {
		return createSelection{}, err
	}
	if err := applyAlias("runtime", selection.Runtime, "agent", input.LegacyAgent, identity); err != nil {
		return createSelection{}, err
	}
	modeMap := func(value string) string {
		return map[string]string{"agent": "agent", "local": "deterministic", "server": "cloud", "session": "session"}[value]
	}
	if input.Set["mode"] {
		mapped := modeMap(strings.ToLower(strings.TrimSpace(input.LegacyMode)))
		if mapped == "" {
			return createSelection{}, errors.New("deprecated --mode must be agent, local, server, or session")
		}
		if mapped == "session" {
			if input.Set["generator"] {
				return createSelection{}, errors.New("--generator cannot be combined with deprecated --mode session")
			}
			selection.SessionPath = true
		} else if input.Set["generator"] && selection.Generator != mapped {
			return createSelection{}, fmt.Errorf("--generator %s conflicts with deprecated --mode %s", selection.Generator, input.LegacyMode)
		} else {
			selection.Generator = mapped
		}
		selection.Deprecated = append(selection.Deprecated, "--mode is deprecated; use --generator or `handoff session locate`")
	}
	if input.Set["compact"] {
		mapped := map[string]string{"current": "agent", "none": "deterministic", "server": "cloud"}[strings.ToLower(strings.TrimSpace(input.LegacyCompact))]
		if mapped == "" {
			return createSelection{}, errors.New("deprecated --compact must be current, none, or server")
		}
		if (input.Set["generator"] || input.Set["mode"]) && selection.Generator != mapped {
			return createSelection{}, fmt.Errorf("generator selection conflicts with deprecated --compact %s", input.LegacyCompact)
		}
		selection.Generator = mapped
		selection.Deprecated = append(selection.Deprecated, "--compact is deprecated; use --generator")
	}
	legacyUpload := ""
	if input.LegacyIncludeTranscript {
		legacyUpload = "selected"
		selection.Deprecated = append(selection.Deprecated, "--include-transcript is deprecated; cloud generation now implies transient sanitized Context processing")
	}
	if input.LegacyFullSession {
		legacyUpload = "full"
		selection.Deprecated = append(selection.Deprecated, "--full-session is deprecated; canonical Context is now always complete (use --attach-context to persist it)")
	}
	if legacyUpload != "" {
		if input.Set["upload-context"] && selection.UploadContext != legacyUpload {
			return createSelection{}, errors.New("--upload-context conflicts with deprecated upload flags")
		}
		selection.UploadContext = legacyUpload
	}
	if input.Set["upload-context"] {
		selection.Deprecated = append(selection.Deprecated, "--upload-context is deprecated; cloud generation now always uses canonical Context transiently")
	}
	if input.Set["stdin"] {
		selection.Deprecated = append(selection.Deprecated, "--stdin is deprecated; pipe input without the flag")
	}
	return selection, nil
}

// resolveCreateMode remains as a compatibility helper for downstream tests and
// integrations. New code should use resolveCreateSelection.
func resolveCreateMode(mode, legacyCompact string, modeExplicit, compactExplicit bool) (string, error) {
	set := map[string]bool{"mode": modeExplicit, "compact": compactExplicit}
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: "auto", Generator: "agent", Runtime: "auto",
		LegacyMode: mode, LegacyCompact: legacyCompact, Set: set,
	})
	if err != nil {
		return "", err
	}
	if selection.SessionPath {
		return "session", nil
	}
	return map[string]string{"agent": "agent", "deterministic": "local", "cloud": "server"}[selection.Generator], nil
}

func runSession(_ string, outputFormat string, args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		fmt.Print(sessionUsage)
		return nil
	}
	if args[0] != "locate" {
		return fmt.Errorf("unknown session command %q; run `handoff session --help`", args[0])
	}
	flags := flag.NewFlagSet("handoff session locate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stdout, sessionUsage) }
	sourceName := flags.String("source", "auto", "context source: auto, codex, claude, or pi")
	goal := flags.String("goal", "", "optional next goal included in the local message")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("usage: handoff session locate [--source auto|codex|claude|pi] [--goal TEXT]")
	}
	contextSource, err := source.Load(source.Options{Kind: *sourceName, NoGit: true})
	if err != nil {
		return err
	}
	return printLocalSession(card.SanitizeGoal(*goal), contextSource, outputFormat == "json", false)
}

func printLocalSession(goal string, sourceContext types.Context, jsonOutput, dryRun bool) error {
	path := strings.TrimSpace(sourceContext.SessionPath)
	if path == "" {
		return errors.New("the selected source does not expose a local Agent Session path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if jsonOutput {
		return printJSON(map[string]any{
			"dry_run":      dryRun,
			"goal":         goal,
			"action":       "session.locate",
			"source":       sourceContext.Source,
			"session_id":   sourceContext.SessionID,
			"session_path": absolute,
			"local_only":   true,
			"uploaded":     false,
		})
	}
	fmt.Print(formatLocalSessionMessage(goal, sourceContext.Source, absolute))
	return nil
}

func formatLocalSessionMessage(goal, sourceKind, sessionPath string) string {
	var message strings.Builder
	message.WriteString("📍 **Local Session（仅限本机）**\n\n")
	fmt.Fprintf(&message, "请让 Agent 直接读取这个 %s Session 文件：\n\n`%s`\n\n", sourceKind, sessionPath)
	if strings.TrimSpace(goal) != "" {
		fmt.Fprintf(&message, "下一目标：%s\n\n", goal)
	}
	message.WriteString("该文件不会上传，也不会生成分享码；其他设备无法访问这个本机路径，请勿把原始 Session 文件发送到公开渠道。\n")
	return message.String()
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

func runReceive(profileName, outputFormat string, args []string) error {
	idArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff receive", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprint(os.Stdout, "Fetch and print an immutable handoff.\n\nUsage:\n  handoff receive <reference|code|url> [--output HANDOFF.md] [--force]\n\nRisk: read\n")
	}
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
	if outputFormat == "json" || *jsonOutput {
		return printJSON(result)
	}
	fmt.Print(result.Handoff.Markdown)
	return nil
}

func runContext(profileName, outputFormat string, args []string) error {
	idArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff context", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprint(os.Stdout, "Fetch the full sanitized readable Context explicitly attached to a Handoff.\n\nUsage:\n  handoff context <reference|code|url> [--output CONTEXT.md] [--force]\n\nRisk: read\n")
	}
	jsonOutput := flags.Bool("json", false, "print structured JSON")
	output := flags.String("output", "", "write readable Context Markdown to a file")
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
		return errors.New("usage: handoff context <code-or-url>")
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := (client.Client{Server: profile.Server}).GetContext(ctx, id)
	if err != nil {
		return err
	}
	if outputFormat == "json" || *jsonOutput {
		return printJSON(result)
	}
	rendered := formatAttachedContext(result)
	if *output != "" {
		if err := writeOutput(*output, rendered, *force); err != nil {
			return err
		}
		absolute, _ := filepath.Abs(*output)
		fmt.Println(absolute)
		return nil
	}
	fmt.Print(rendered)
	return nil
}

func formatAttachedContext(result types.ContextResponse) string {
	attachment := result.Context
	var output strings.Builder
	output.WriteString("# Attached Handoff Context\n\n")
	fmt.Fprintf(&output, "- Handoff: `opengrove-handoff:%s`\n", result.HandoffID)
	fmt.Fprintf(&output, "- Source: `%s`\n", attachment.Source.Kind)
	fmt.Fprintf(&output, "- Redaction: `%s` (best effort)\n", attachment.Redaction)
	fmt.Fprintf(&output, "- Messages: %d\n\n", len(attachment.Messages))
	if strings.TrimSpace(attachment.NativeSummary) != "" {
		output.WriteString("## Native Compact Summary (auxiliary)\n\n")
		output.WriteString(attachment.NativeSummary)
		output.WriteString("\n\n")
	}
	output.WriteString("## Readable Conversation\n\n")
	for _, message := range attachment.Messages {
		role := strings.ToUpper(strings.TrimSpace(message.Role))
		if role == "" {
			role = "MESSAGE"
		}
		fmt.Fprintf(&output, "### %s", role)
		if !message.At.IsZero() {
			fmt.Fprintf(&output, " · %s", message.At.Format(time.RFC3339))
		}
		output.WriteString("\n\n")
		output.WriteString(message.Text)
		output.WriteString("\n\n")
	}
	output.WriteString("> 这是 Handoff 附带的、经过尽力脱敏的可读 Context，不是包含工具结果和内部记录的原始 Provider Session。\n")
	return output.String()
}

func runDelete(profileName, outputFormat string, args []string) error {
	idArgument, args := leadingArgument(args)
	flags := flag.NewFlagSet("handoff delete", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprint(os.Stdout, "Permanently delete an exact handoff before expiry.\n\nUsage:\n  handoff delete <reference|code|url> --yes\n\nRisk: high-risk-write\n")
	}
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
	id, referenceServer := parseHandoffRef(idArgument)
	if id == "" {
		return errors.New("invalid handoff code or URL")
	}
	if !*yes {
		return &structuredError{ExitCode: 10, Payload: map[string]any{
			"ok": false,
			"error": map[string]any{
				"type":    "confirmation_required",
				"message": "deleting a handoff is irreversible",
				"hint":    "append --yes only after the user explicitly confirms the exact handoff",
				"risk": map[string]any{
					"level":  "high-risk-write",
					"action": "handoff delete",
					"target": agentReference(id),
				},
			},
			"_meta": map[string]any{"envelope_version": "1.0", "risk": "high-risk-write", "danger": true},
		}}
	}
	_, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	if referenceServer != "" {
		profile.Server = referenceServer
	}
	deleteToken, err := ownership.Get(profile.Server, id)
	if err != nil {
		return fmt.Errorf("read local delete credential: %w", err)
	}
	if deleteToken == "" && profile.Token == "" {
		return errors.New("no local delete credential for this handoff; only its creator or a service administrator can delete it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := (client.Client{Server: profile.Server, Token: profile.Token, DeleteToken: deleteToken}).Delete(ctx, id); err != nil {
		return err
	}
	if deleteToken != "" {
		_ = ownership.Remove(profile.Server, id)
	}
	if outputFormat == "json" {
		return printJSON(map[string]any{"deleted": true, "id": id, "server": profile.Server})
	}
	fmt.Println("Handoff deleted.")
	return nil
}

func runAdmin(profileName, outputFormat string, args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		fmt.Print(adminUsage)
		return nil
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
		flags := flag.NewFlagSet("handoff admin login", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		flags.Usage = func() { fmt.Print(adminUsage) }
		server := flags.String("server", profile.Server, "handoffd base URL")
		tokenStdin := flags.Bool("token-stdin", false, "read API token from stdin")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if len(flags.Args()) != 0 {
			return errors.New("usage: handoff admin login [--server URL] [--token-stdin]")
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
		if outputFormat == "json" {
			return printJSON(map[string]any{"logged_in": true, "profile": name, "server": strings.TrimRight(*server, "/")})
		}
		fmt.Printf("Logged in to %s as profile %q.\n", strings.TrimRight(*server, "/"), name)
		return nil
	case "status":
		if len(args) != 1 {
			if wantsHelp(args[1:]) {
				fmt.Print(adminUsage)
				return nil
			}
			return errors.New("usage: handoff admin status")
		}
		_, openGroveAuthErr := opengroveauth.AccessToken(time.Now())
		status := map[string]any{
			"profile": name, "server": profile.Server,
			"opengrove_authenticated": openGroveAuthErr == nil,
			"admin_token_configured":  profile.Token != "",
		}
		if outputFormat == "text" {
			fmt.Printf("OpenGrove login: %s\nServer: %s\nAdministrator credential: %s\n",
				boolWord(openGroveAuthErr == nil), profile.Server, boolWord(profile.Token != ""))
			return nil
		}
		return printJSON(status)
	case "logout":
		if len(args) != 1 {
			if wantsHelp(args[1:]) {
				fmt.Print(adminUsage)
				return nil
			}
			return errors.New("usage: handoff admin logout")
		}
		profile.Token = ""
		cfg.Profiles[name] = profile
		if err := config.Save(cfg); err != nil {
			return err
		}
		if outputFormat == "json" {
			return printJSON(map[string]any{"logged_out": true, "profile": name})
		}
		fmt.Printf("Logged out profile %q.\n", name)
		return nil
	default:
		return fmt.Errorf("unknown admin command %q; run `handoff admin --help`", args[0])
	}
}

func runConfig(profileName, outputFormat string, args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		fmt.Print(configUsage)
		return nil
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
		if len(args) != 1 {
			if wantsHelp(args[1:]) {
				fmt.Print(configUsage)
				return nil
			}
			return errors.New("usage: handoff config show")
		}
		path, _ := config.Path()
		value := map[string]any{"profile": name, "server": profile.Server, "admin_token_configured": profile.Token != "", "path": path}
		if outputFormat == "text" {
			fmt.Printf("Profile: %s\nServer: %s\nConfig: %s\n", name, profile.Server, path)
			return nil
		}
		return printJSON(value)
	case "set-server":
		if wantsHelp(args[1:]) {
			fmt.Print(configUsage)
			return nil
		}
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
		if outputFormat == "json" {
			return printJSON(map[string]any{"profile": name, "server": profile.Server})
		}
		fmt.Println(profile.Server)
		return nil
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func runDoctor(profileName, outputFormat string, args []string) error {
	flags := flag.NewFlagSet("handoff doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprint(os.Stdout, "Check source discovery, identity, and connectivity.\n\nUsage:\n  handoff doctor [--offline]\n\nRisk: read\n")
	}
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
	_, openGroveAuthErr := opengroveauth.AccessToken(time.Now())
	checks := []map[string]any{
		{"check": "profile", "ok": true, "detail": name},
		{"check": "opengrove_login", "ok": openGroveAuthErr == nil, "detail": boolDetail(openGroveAuthErr == nil, "available for cloud generation", "not logged in; only cloud generation is unavailable")},
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
	failed := false
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
	if outputFormat == "text" {
		for _, check := range checks {
			fmt.Printf("%s %s: %s\n", boolMarker(check["ok"] == true), check["check"], check["detail"])
		}
	} else if err := printJSON(map[string]any{"ok": !failed, "checks": checks}); err != nil {
		return err
	}
	if failed {
		return errors.New("doctor found blocking problems")
	}
	return nil
}

func runWhoAmI(profileName, outputFormat string, args []string) error {
	if wantsHelp(args) {
		fmt.Print("Show OpenGrove identity and effective Handoff capabilities.\n\nUsage:\n  handoff whoami\n\nRisk: read\n")
		return nil
	}
	if len(args) != 0 {
		return errors.New("usage: handoff whoami")
	}
	name, profile, err := loadProfile(profileName)
	if err != nil {
		return err
	}
	token, tokenErr := opengroveauth.AccessToken(time.Now())
	authenticated := false
	var user opengroveauth.User
	var verificationError string
	if tokenErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		baseURL := strings.TrimSpace(os.Getenv("OPENGROVE_WW_BASE_URL"))
		user, err = opengroveauth.CurrentUser(ctx, baseURL, token, nil)
		if err != nil {
			verificationError = err.Error()
		} else {
			authenticated = user.UserID != ""
		}
	}
	value := map[string]any{
		"cli": map[string]any{
			"name":    "opengrove-handoff",
			"version": version,
		},
		"profile": name,
		"server":  profile.Server,
		"opengrove": map[string]any{
			"authenticated": authenticated,
			"user":          user,
			"error":         verificationError,
		},
		"capabilities": map[string]any{
			"publish":            true,
			"receive":            true,
			"cloud_generation":   authenticated,
			"publish_login":      "not required",
			"cloud_login":        "OpenGrove required",
			"administrator_mode": profile.Token != "",
		},
	}
	if outputFormat == "json" {
		return printJSON(value)
	}
	identity := "未登录"
	if authenticated {
		identity = user.Email
		if identity == "" {
			identity = user.UserID
		}
	}
	fmt.Printf("Handoff: v%s\nServer: %s\nOpenGrove: %s\n云端生成: %s\n匿名发布与读取: 可用\n",
		version, profile.Server, identity, boolAvailability(authenticated))
	if verificationError != "" {
		fmt.Println("登录校验异常: " + verificationError)
	}
	return nil
}

func runUpdate(outputFormat string, args []string) error {
	flags := flag.NewFlagSet("handoff update", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		fmt.Fprint(os.Stdout, "Install a verified GitHub release and synchronize its Agent Skill.\n\nUsage:\n  handoff update [--check] [--force]\n\nRisk: high-risk-write\n")
	}
	checkOnly := flags.Bool("check", false, "only check for updates")
	force := flags.Bool("force", false, "reinstall the latest release")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("usage: handoff update [--check] [--force]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	updateClient := updater.Client{Token: updater.GitHubToken()}
	result, err := updateClient.Check(ctx, version)
	if err != nil {
		return err
	}
	if *checkOnly || !result.UpdateAvailable && !*force {
		if outputFormat == "json" {
			return printJSON(result)
		}
		if result.UpdateAvailable {
			fmt.Printf("Handoff %s is available (current %s). Run `handoff update` to install it.\n", result.LatestVersion, result.CurrentVersion)
		} else {
			fmt.Printf("Handoff %s is up to date.\n", result.CurrentVersion)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := updateClient.Apply(ctx, result, executable); err != nil {
		return err
	}
	currentSkill, _ := skillbundle.Read("handoff")
	currentMetadata, _ := skillbundle.OpenAIYAML("handoff")
	skillSync := syncUpdatedSkill(ctx, executable, currentSkill, currentMetadata)
	if outputFormat == "json" {
		return printJSON(map[string]any{
			"updated":          true,
			"previous_version": result.CurrentVersion,
			"version":          result.LatestVersion,
			"release_url":      result.ReleaseURL,
			"skill_sync":       skillSync,
		})
	}
	fmt.Printf("Updated Handoff %s → %s.\n", result.CurrentVersion, result.LatestVersion)
	if len(skillSync.Installed) > 0 {
		fmt.Printf("Synchronized Agent Skill: %s\n", strings.Join(skillSync.Installed, ", "))
	}
	if len(skillSync.SkippedCustom) > 0 {
		fmt.Printf("Custom Skill preserved: %s (run `handoff skills install --force` to replace it)\n", strings.Join(skillSync.SkippedCustom, ", "))
	}
	if len(skillSync.Errors) > 0 {
		fmt.Printf("Skill sync warning: %s\n", strings.Join(skillSync.Errors, "; "))
	}
	fmt.Println("Restart running Agent sessions before relying on new Skill behavior.")
	return nil
}

func runSchema(outputFormat string, args []string) error {
	if wantsHelp(args) {
		fmt.Print("Print an exact machine-readable command contract.\n\nUsage:\n  handoff schema [action]\n\nExamples:\n  handoff schema create\n  handoff schema session.locate\n  handoff schema admin.login\n\nRisk: read\n")
		return nil
	}
	if len(args) > 1 {
		return errors.New("usage: handoff schema [action]")
	}
	commands := []string{
		"create", "session.locate", "receive", "context", "delete",
		"admin.login", "admin.status", "admin.logout",
		"config.show", "config.set-server", "doctor", "whoami",
		"update", "skills.list", "skills.read", "skills.install", "version",
	}
	if len(args) == 0 {
		return printJSON(map[string]any{
			"ok":       true,
			"commands": commands,
			"hint":     "run `handoff schema <action>` for its exact JSON Schema contract",
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
					"goal":           stringProperty("Short next goal for the receiver; the complete value remains in the Agent section."),
					"source":         map[string]any{"type": "string", "enum": []string{"auto", "codex", "claude", "pi"}, "default": "auto"},
					"generator":      map[string]any{"type": "string", "enum": []string{"agent", "deterministic", "cloud"}, "default": "agent"},
					"runtime":        map[string]any{"type": "string", "enum": []string{"auto", "codex", "claude", "pi"}, "default": "auto", "description": "Selects an Agent runtime, never a model."},
					"attach_context": booleanProperty("Persist the complete sanitized readable Context beside the handoff, independently of the generator."),
					"review":         booleanProperty("Edit generated Markdown before publishing."),
					"file":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repeatable context file path."},
					"no_git":         booleanProperty("Omit repository metadata."),
					"ttl":            map[string]any{"type": "string", "default": "168h", "description": "Go duration for the handoff lifetime."},
					"json":           booleanProperty("Print machine-readable output."),
					"dry_run":        booleanProperty("Inspect source and upload behavior without an Agent or network write."),
					"output":         stringProperty("Also write HANDOFF.md to this path."),
					"force":          booleanProperty("Allow overwriting the output file."),
				},
			},
			"outputSchema": createOutputSchema(),
			"_meta": map[string]any{
				"envelope_version": "1.0", "risk": "write", "danger": false,
				"session":              "read-only snapshot; never compacted, resumed, or modified",
				"default_upload":       "generated sections only; --attach-context is the explicit persistence boundary",
				"default_model_config": "inherits the current Agent runtime",
				"canonical_context":    "all readable user/assistant messages after normalization and best-effort redaction; excludes thinking and tool results",
				"native_compact":       "a readable native compact summary is auxiliary evidence; it never replaces the canonical message history and native /compact is never invoked",
				"cloud_auth":           "requires an active local OpenGrove login; publishing does not require login",
				"cloud_processing":     "cloud generation temporarily receives canonical sanitized Context and does not persist it unless --attach-context is also set",
				"context_attachment":   "explicit opt-in; readable messages only, no thinking or tool results; best-effort redaction cannot guarantee removal of every natural-language identifier",
				"legacy":               "--upload-context, --mode, --from, --agent, --include-transcript, --full-session, --stdin, and --compact are accepted temporarily but omitted from the preferred contract",
			},
		}, nil
	case "session.locate":
		return map[string]any{
			"name": "handoff session locate", "description": "Return a same-machine provider Session path without upload or generation.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"source": map[string]any{"type": "string", "enum": []string{"auto", "codex", "claude", "pi"}, "default": "auto"},
					"goal":   stringProperty("Optional next goal included in the text response."),
				},
			},
			"outputSchema": localSessionOutputSchema(), "_meta": meta("read"),
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
			"outputSchema": receiveOutputSchema(), "_meta": meta("read"),
		}, nil
	case "context":
		return map[string]any{
			"name": "handoff context", "description": "Fetch the complete sanitized readable Context explicitly attached to a handoff.",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"code_or_url"}, "additionalProperties": false,
				"properties": map[string]any{
					"code_or_url": stringProperty("Branded opengrove-handoff reference, share code, or human URL."),
					"json":        booleanProperty("Print structured JSON instead of readable Markdown."),
					"output":      stringProperty("Write readable Context Markdown to this path."),
					"force":       booleanProperty("Allow overwriting the output file."),
				},
			},
			"outputSchema": contextOutputSchema(), "_meta": meta("read"),
		}, nil
	case "delete":
		return map[string]any{
			"name": "handoff delete", "description": "Permanently delete a handoff before expiry using its locally saved owner credential or an administrator credential.",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"code_or_url", "yes"}, "additionalProperties": false,
				"properties": map[string]any{
					"code_or_url": stringProperty("Exact handoff share code or URL to delete."),
					"yes":         map[string]any{"type": "boolean", "const": true, "description": "Explicit confirmation after user approval."},
				},
			},
			"outputSchema": objectSchema(map[string]any{
				"deleted": map[string]any{"type": "boolean", "const": true},
				"id":      stringProperty("Deleted Handoff capability code."),
				"server":  map[string]any{"type": "string", "format": "uri"},
			}, "deleted", "id", "server"),
			"_meta": meta("high-risk-write"),
		}, nil
	case "admin.login":
		return commandContractWithOutput("handoff admin login", "Store an optional service administrator credential. This is not OpenGrove login.", "write", map[string]any{
			"server":      stringProperty("Handoff service URL for administrator login."),
			"token_stdin": booleanProperty("Read the administrator token from stdin."),
		}, nil, objectSchema(map[string]any{
			"logged_in": map[string]any{"type": "boolean", "const": true},
			"profile":   stringProperty("Active configuration profile."),
			"server":    map[string]any{"type": "string", "format": "uri"},
		}, "logged_in", "profile", "server")), nil
	case "admin.status":
		return commandContractWithOutput("handoff admin status", "Show whether an administrator credential is configured.", "read", map[string]any{}, nil, objectSchema(map[string]any{
			"profile":                 stringProperty("Active configuration profile."),
			"server":                  map[string]any{"type": "string", "format": "uri"},
			"opengrove_authenticated": booleanProperty("Whether local OpenGrove user login is available for cloud generation."),
			"admin_token_configured":  booleanProperty("Whether a service administrator credential is configured."),
		}, "profile", "server", "opengrove_authenticated", "admin_token_configured")), nil
	case "admin.logout":
		return commandContractWithOutput("handoff admin logout", "Remove the local service administrator credential.", "write", map[string]any{}, nil, objectSchema(map[string]any{
			"logged_out": map[string]any{"type": "boolean", "const": true},
			"profile":    stringProperty("Configuration profile whose administrator credential was removed."),
		}, "logged_out", "profile")), nil
	case "config.show":
		return commandContractWithOutput("handoff config show", "Show the effective Handoff profile.", "read", map[string]any{}, nil, objectSchema(map[string]any{
			"profile":                stringProperty("Active configuration profile."),
			"server":                 map[string]any{"type": "string", "format": "uri"},
			"admin_token_configured": booleanProperty("Whether a service administrator credential is configured."),
			"path":                   stringProperty("Absolute local configuration file path."),
		}, "profile", "server", "admin_token_configured", "path")), nil
	case "config.set-server":
		return commandContractWithOutput("handoff config set-server", "Set the Handoff service URL.", "write", map[string]any{
			"server": stringProperty("Absolute HTTPS Handoff service URL."),
		}, []string{"server"}, objectSchema(map[string]any{
			"profile": stringProperty("Updated configuration profile."),
			"server":  map[string]any{"type": "string", "format": "uri"},
		}, "profile", "server")), nil
	case "doctor":
		return commandContractWithOutput("handoff doctor", "Check session discovery, current Agent, OpenGrove login, and service connectivity.", "read", map[string]any{
			"offline": booleanProperty("Skip service connectivity."),
		}, nil, objectSchema(map[string]any{
			"ok": map[string]any{"type": "boolean"},
			"checks": map[string]any{
				"type": "array",
				"items": objectSchema(map[string]any{
					"check":  stringProperty("Stable check name."),
					"ok":     map[string]any{"type": "boolean"},
					"detail": stringProperty("Human-readable result."),
				}, "check", "ok", "detail"),
			},
		}, "ok", "checks")), nil
	case "whoami":
		return commandContractWithOutput("handoff whoami", "Show CLI version, service, OpenGrove identity, and cloud-generation availability.", "read", map[string]any{}, nil, whoAmIOutputSchema()), nil
	case "update":
		return commandContractWithOutput("handoff update", "Check for or install a SHA-256 verified GitHub release.", "high-risk-write", map[string]any{
			"check": booleanProperty("Only check; do not replace the executable."),
			"force": booleanProperty("Reinstall the latest release."),
		}, nil, updateOutputSchema()), nil
	case "skills.list":
		return commandContractWithOutput("handoff skills list", "List version-matched Agent Skills embedded in the CLI.", "read", map[string]any{}, nil, objectSchema(map[string]any{
			"ok":     map[string]any{"type": "boolean", "const": true},
			"skills": map[string]any{"type": "array", "items": skillSchema()},
			"count":  map[string]any{"type": "integer", "minimum": 0},
		}, "ok", "skills", "count")), nil
	case "skills.read":
		return commandContractWithOutput("handoff skills read", "Read one embedded Agent Skill.", "read", map[string]any{
			"name": stringProperty("Embedded Skill name; currently handoff."),
		}, []string{"name"}, objectSchema(map[string]any{
			"ok":      map[string]any{"type": "boolean", "const": true},
			"name":    stringProperty("Embedded Skill name."),
			"content": stringProperty("Complete SKILL.md content."),
		}, "ok", "name", "content")), nil
	case "skills.install":
		return commandContractWithOutput("handoff skills install", "Install or repair one embedded Agent Skill.", "write", map[string]any{
			"name":   stringProperty("Embedded Skill name; currently handoff."),
			"target": map[string]any{"type": "string", "enum": []string{"all", "codex", "claude", "agents"}, "default": "all"},
			"force":  booleanProperty("Overwrite a different installed Skill."),
		}, nil, objectSchema(map[string]any{
			"ok":        map[string]any{"type": "boolean", "const": true},
			"name":      stringProperty("Installed Skill name."),
			"target":    map[string]any{"type": "string", "enum": []string{"all", "codex", "claude", "agents"}},
			"installed": map[string]any{"type": "array", "items": stringProperty("Absolute installed SKILL.md path.")},
		}, "ok", "name", "target", "installed")), nil
	case "auth":
		addDeprecationNotice("schema `auth` is deprecated; use admin.login, admin.status, or admin.logout")
		return commandContract("handoff admin", "Deprecated group contract; inspect an exact admin action.", "write", map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"login", "status", "logout"}},
		}, []string{"action"}), nil
	case "config":
		return commandContract("handoff config", "Group contract; inspect config.show or config.set-server.", "write", map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"show", "set-server"}},
		}, []string{"action"}), nil
	case "skills":
		return commandContract("handoff skills", "Group contract; inspect skills.list, skills.read, or skills.install.", "write", map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "read", "install"}},
		}, []string{"action"}), nil
	case "version":
		return commandContractWithOutput("handoff version", "Print the CLI version.", "read", map[string]any{}, nil, objectSchema(map[string]any{
			"version": stringProperty("Installed CLI version."),
		}, "version")), nil
	default:
		return nil, fmt.Errorf("unknown schema %q; run `handoff schema` to list contracts", command)
	}
}

func commandContract(name, description, risk string, properties map[string]any, required []string) map[string]any {
	return commandContractWithOutput(name, description, risk, properties, required, map[string]any{"type": "object"})
}

func commandContractWithOutput(name, description, risk string, properties map[string]any, required []string, output map[string]any) map[string]any {
	input := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		input["required"] = required
	}
	return map[string]any{
		"name":         name,
		"description":  description,
		"inputSchema":  input,
		"outputSchema": output,
		"_meta": map[string]any{
			"envelope_version": "1.0",
			"risk":             risk,
			"danger":           risk == "high-risk-write",
		},
	}
}

func createOutputSchema() map[string]any {
	handoffOutput := receiveOutputSchema()
	properties := handoffOutput["properties"].(map[string]any)
	properties["share_message"] = map[string]any{"type": "string", "description": "Canonical user-facing Markdown. Agents must relay this value verbatim without rewriting."}
	properties["agent_reference"] = map[string]any{"type": "string", "pattern": "^opengrove-handoff:[A-Za-z0-9_-]{20,32}$"}
	properties["delete_credential_saved"] = map[string]any{
		"type":        "boolean",
		"description": "True when the private per-handoff delete credential was saved locally; the credential itself is never printed.",
	}
	properties["fallback_used"] = map[string]any{
		"type":        "boolean",
		"description": "True when the requested Agent generator failed and deterministic extraction was used.",
	}
	properties["generation_warning"] = map[string]any{
		"type":        "string",
		"description": "Redacted Agent generation failure when fallback_used is true.",
	}
	handoffOutput["required"] = []string{"handoff", "share_url", "markdown_url", "agent_reference", "share_message", "delete_credential_saved", "fallback_used"}
	return handoffOutput
}

func receiveOutputSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"handoff"},
		"properties": map[string]any{
			"handoff":      handoffSchema(),
			"share_url":    map[string]any{"type": "string", "format": "uri"},
			"markdown_url": map[string]any{"type": "string", "format": "uri"},
		},
	}
}

func handoffSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"version", "id", "goal", "markdown", "generator", "created_at", "expires_at"},
		"properties": map[string]any{
			"version":    map[string]any{"type": "integer"},
			"id":         map[string]any{"type": "string"},
			"title":      map[string]any{"type": "string"},
			"goal":       map[string]any{"type": "string"},
			"markdown":   map[string]any{"type": "string"},
			"generator":  map[string]any{"type": "string"},
			"context":    map[string]any{"type": "object", "description": "Present only when a full sanitized Context attachment is available."},
			"created_at": map[string]any{"type": "string", "format": "date-time"},
			"expires_at": map[string]any{"type": "string", "format": "date-time"},
		},
	}
}

func contextOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"handoff_id": map[string]any{"type": "string"},
		"context": objectSchema(map[string]any{
			"version":              map[string]any{"type": "integer"},
			"source":               map[string]any{"type": "object"},
			"native_summary":       map[string]any{"type": "string"},
			"native_compact_found": map[string]any{"type": "boolean"},
			"messages":             map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"repository":           map[string]any{"type": "object"},
			"redaction":            map[string]any{"type": "string"},
		}, "version", "source", "messages", "redaction"),
	}, "handoff_id", "context")
}

func localSessionOutputSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"action", "source", "session_path", "local_only", "uploaded"},
		"properties": map[string]any{
			"action":       map[string]any{"type": "string", "const": "session.locate"},
			"source":       map[string]any{"type": "string", "enum": []string{"codex", "claude", "pi"}},
			"session_id":   map[string]any{"type": "string"},
			"session_path": map[string]any{"type": "string", "description": "Absolute same-machine provider Session path; never uploaded."},
			"local_only":   map[string]any{"type": "boolean", "const": true},
			"uploaded":     map[string]any{"type": "boolean", "const": false},
			"dry_run":      map[string]any{"type": "boolean"},
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func whoAmIOutputSchema() map[string]any {
	return objectSchema(map[string]any{
		"cli": objectSchema(map[string]any{
			"name":    map[string]any{"type": "string", "const": "opengrove-handoff"},
			"version": map[string]any{"type": "string"},
		}, "name", "version"),
		"profile": map[string]any{"type": "string"},
		"server":  map[string]any{"type": "string", "format": "uri"},
		"opengrove": objectSchema(map[string]any{
			"authenticated": map[string]any{"type": "boolean"},
			"user":          map[string]any{"type": "object"},
			"error":         map[string]any{"type": "string"},
		}, "authenticated", "user", "error"),
		"capabilities": objectSchema(map[string]any{
			"publish":            map[string]any{"type": "boolean"},
			"receive":            map[string]any{"type": "boolean"},
			"cloud_generation":   map[string]any{"type": "boolean"},
			"publish_login":      map[string]any{"type": "string"},
			"cloud_login":        map[string]any{"type": "string"},
			"administrator_mode": map[string]any{"type": "boolean"},
		}, "publish", "receive", "cloud_generation", "publish_login", "cloud_login", "administrator_mode"),
	}, "cli", "profile", "server", "opengrove", "capabilities")
}

func updateOutputSchema() map[string]any {
	check := objectSchema(map[string]any{
		"current_version":  map[string]any{"type": "string"},
		"latest_version":   map[string]any{"type": "string"},
		"update_available": map[string]any{"type": "boolean"},
		"release_url":      map[string]any{"type": "string", "format": "uri"},
		"asset_name":       map[string]any{"type": "string"},
	}, "current_version", "latest_version", "update_available")
	installed := objectSchema(map[string]any{
		"updated":          map[string]any{"type": "boolean", "const": true},
		"previous_version": map[string]any{"type": "string"},
		"version":          map[string]any{"type": "string"},
		"release_url":      map[string]any{"type": "string", "format": "uri"},
		"skill_sync": objectSchema(map[string]any{
			"installed":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"skipped_custom": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"errors":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}),
	}, "updated", "previous_version", "version", "release_url", "skill_sync")
	return map[string]any{"oneOf": []any{check, installed}}
}

func skillSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":        map[string]any{"type": "string"},
		"description": map[string]any{"type": "string"},
		"version":     map[string]any{"type": "string"},
		"metadata":    map[string]any{"type": "object"},
	}, "name", "description", "version", "metadata")
}

const skillsUsage = `Read Agent Skills embedded in the handoff CLI so instructions stay in sync with the binary.

Usage:
  handoff skills list [--json]
  handoff skills read <name> [--json]
  handoff skills install [name] [--target all|codex|claude|agents] [--force]

Risk: read/write
`

const skillsListUsage = `List version-matched Agent Skills embedded in the CLI.

Usage:
  handoff skills list [--json]

Risk: read
`

const skillsReadUsage = `Read one version-matched Agent Skill embedded in the CLI.

Usage:
  handoff skills read <name> [--json]

Risk: read
`

const skillsInstallUsage = `Install or repair an embedded Agent Skill.

Usage:
  handoff skills install [name] [--target all|codex|claude|agents] [--force]

Risk: write
`

func runSkills(outputFormat string, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(skillsUsage)
		return nil
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("handoff skills list", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		flags.Usage = func() { fmt.Fprint(os.Stdout, skillsListUsage) }
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
		if outputFormat == "json" || *jsonOutput {
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
		flags.Usage = func() { fmt.Fprint(os.Stdout, skillsReadUsage) }
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
		if outputFormat == "json" || *jsonOutput {
			return printJSON(map[string]any{"ok": true, "name": name, "content": content})
		}
		fmt.Print(content)
		return nil
	case "install":
		name, rest := leadingArgument(args[1:])
		flags := flag.NewFlagSet("handoff skills install", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		flags.Usage = func() { fmt.Fprint(os.Stdout, skillsInstallUsage) }
		target := flags.String("target", "all", "installation target: all, codex, claude, or agents")
		force := flags.Bool("force", false, "overwrite a different installed Skill")
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if name == "" && len(flags.Args()) == 1 {
			name = flags.Args()[0]
		}
		if name == "" {
			name = "handoff"
		}
		if len(flags.Args()) > 0 {
			return errors.New("usage: handoff skills install [name] [--target all|codex|claude|agents] [--force]")
		}
		content, ok := skillbundle.Read(name)
		if !ok {
			return fmt.Errorf("unknown skill %q; run `handoff skills list`", name)
		}
		installed, err := installSkill(name, content, *target, *force)
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			return printJSON(map[string]any{"ok": true, "name": name, "target": *target, "installed": installed})
		}
		for _, path := range installed {
			fmt.Println(path)
		}
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
	data, err := readStdinContext(os.Stdin, 4<<20)
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

func readStdinContext(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("stdin context exceeds the %d MiB limit", limit>>20)
	}
	return data, nil
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
	if match := canonicalHandoffRef.FindStringSubmatch(value); len(match) == 2 {
		return match[1], ""
	}
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
	if len(pendingNotices) > 0 {
		if object, ok := value.(map[string]any); ok {
			copy := make(map[string]any, len(object)+1)
			for key, item := range object {
				copy[key] = item
			}
			if _, exists := copy["_notice"]; !exists {
				copy["_notice"] = pendingNotices
			}
			value = copy
		}
	}
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

func wantsHelp(args []string) bool {
	for _, argument := range args {
		if argument == "--help" || argument == "-h" || argument == "help" {
			return true
		}
	}
	return false
}

func addDeprecationNotice(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if pendingNotices == nil {
		pendingNotices = map[string]any{}
	}
	current, _ := pendingNotices["deprecated"].([]string)
	for _, existing := range current {
		if existing == message {
			return
		}
	}
	pendingNotices["deprecated"] = append(current, message)
}

type updateNoticeCache struct {
	CheckedAt      time.Time `json:"checked_at"`
	CurrentVersion string    `json:"current_version"`
	LatestVersion  string    `json:"latest_version"`
	ReleaseURL     string    `json:"release_url,omitempty"`
}

func maybeAddUpdateNotice() {
	if strings.TrimSpace(os.Getenv("HANDOFF_NO_UPDATE_NOTIFIER")) != "" {
		return
	}
	configPath, err := config.Path()
	if err != nil {
		return
	}
	cachePath := filepath.Join(filepath.Dir(configPath), "update-notice.json")
	var cached updateNoticeCache
	if data, readErr := os.ReadFile(cachePath); readErr == nil && json.Unmarshal(data, &cached) == nil {
		if cached.CurrentVersion == version && time.Since(cached.CheckedAt) >= 0 && time.Since(cached.CheckedAt) < 24*time.Hour {
			addCachedUpdateNotice(cached)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, checkErr := (updater.Client{}).Check(ctx, version)
	if checkErr != nil {
		return
	}
	cached = updateNoticeCache{
		CheckedAt: time.Now().UTC(), CurrentVersion: version,
		LatestVersion: result.LatestVersion, ReleaseURL: result.ReleaseURL,
	}
	if data, marshalErr := json.Marshal(cached); marshalErr == nil {
		if mkdirErr := os.MkdirAll(filepath.Dir(cachePath), 0o700); mkdirErr == nil {
			_ = os.WriteFile(cachePath, append(data, '\n'), 0o600)
		}
	}
	addCachedUpdateNotice(cached)
}

func addCachedUpdateNotice(cached updateNoticeCache) {
	if !versionIsNewer(cached.LatestVersion, cached.CurrentVersion) {
		return
	}
	if pendingNotices == nil {
		pendingNotices = map[string]any{}
	}
	pendingNotices["update"] = map[string]any{
		"current_version": cached.CurrentVersion,
		"latest_version":  cached.LatestVersion,
		"release_url":     cached.ReleaseURL,
		"command":         "handoff update",
	}
}

func versionIsNewer(candidate, current string) bool {
	parse := func(value string) ([3]int, bool) {
		var parsed [3]int
		parts := strings.Split(strings.SplitN(strings.TrimPrefix(strings.TrimSpace(value), "v"), "-", 2)[0], ".")
		if len(parts) != 3 {
			return parsed, false
		}
		for index, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil {
				return parsed, false
			}
			parsed[index] = number
		}
		return parsed, true
	}
	left, leftOK := parse(candidate)
	right, rightOK := parse(current)
	if !leftOK || !rightOK {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return left[index] > right[index]
		}
	}
	return false
}

func boolWord(value bool) string {
	if value {
		return "已配置"
	}
	return "未配置"
}

func boolAvailability(value bool) string {
	if value {
		return "可用"
	}
	return "不可用（需要 OpenGrove 登录）"
}

func boolMarker(value bool) string {
	if value {
		return "✓"
	}
	return "✗"
}

func createCommandOutput(result types.CreateResponse, credentialSaved bool, generationWarning error) map[string]any {
	output := map[string]any{
		"handoff":                 result.Handoff,
		"share_url":               result.ShareURL,
		"markdown_url":            result.MarkdownURL,
		"agent_reference":         agentReference(result.Handoff.ID),
		"share_message":           formatShareMessage(result),
		"delete_credential_saved": credentialSaved,
		"fallback_used":           generationWarning != nil,
	}
	if generationWarning != nil {
		output["generation_warning"] = card.Redact(generationWarning.Error())
	}
	return output
}

func agentReference(id string) string {
	return "opengrove-handoff:" + strings.TrimSpace(id)
}

func extractOutputFormat(args []string) ([]string, string, error) {
	cleaned := make([]string, 0, len(args))
	format := ""
	setFormat := func(value string) error {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "text" && value != "json" {
			return errors.New("--format must be text or json")
		}
		if format != "" && format != value {
			return errors.New("--json and --format specify conflicting output formats")
		}
		format = value
		return nil
	}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--json":
			if err := setFormat("json"); err != nil {
				return nil, "", err
			}
		case argument == "--format":
			if index+1 >= len(args) {
				return nil, "", errors.New("--format requires text or json")
			}
			index++
			if err := setFormat(args[index]); err != nil {
				return nil, "", err
			}
		case strings.HasPrefix(argument, "--format="):
			if err := setFormat(strings.TrimPrefix(argument, "--format=")); err != nil {
				return nil, "", err
			}
		default:
			cleaned = append(cleaned, argument)
		}
	}
	if format == "" {
		format = "text"
	}
	return cleaned, format, nil
}

type skillSyncResult struct {
	Installed     []string `json:"installed,omitempty"`
	SkippedCustom []string `json:"skipped_custom,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

func syncUpdatedSkill(ctx context.Context, executable, previousContent, previousMetadata string) skillSyncResult {
	var result skillSyncResult
	paths, err := skillPaths("handoff")
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	for _, target := range []string{"codex", "claude", "agents"} {
		path := paths[target]
		existing, readErr := os.ReadFile(path)
		if readErr == nil && string(existing) != previousContent {
			result.SkippedCustom = append(result.SkippedCustom, target)
			continue
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			result.Errors = append(result.Errors, target+": "+readErr.Error())
			continue
		}
		metadataPath := filepath.Join(filepath.Dir(path), "agents", "openai.yaml")
		metadata, metadataErr := os.ReadFile(metadataPath)
		if metadataErr == nil && string(metadata) != previousMetadata {
			result.SkippedCustom = append(result.SkippedCustom, target)
			continue
		}
		if metadataErr != nil && !errors.Is(metadataErr, os.ErrNotExist) {
			result.Errors = append(result.Errors, target+": "+metadataErr.Error())
			continue
		}
		command := exec.CommandContext(ctx, executable, "skills", "install", "handoff", "--target", target, "--force", "--json")
		command.Env = append(os.Environ(), "HANDOFF_NO_UPDATE_NOTIFIER=1")
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = commandErr.Error()
			}
			result.Errors = append(result.Errors, target+": "+message)
			continue
		}
		result.Installed = append(result.Installed, target)
	}
	return result
}

func skillPaths(name string) (map[string]string, error) {
	home := strings.TrimSpace(os.Getenv("HANDOFF_SKILL_HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	return map[string]string{
		"codex":  filepath.Join(home, ".codex", "skills", name, "SKILL.md"),
		"claude": filepath.Join(home, ".claude", "skills", name, "SKILL.md"),
		"agents": filepath.Join(home, ".agents", "skills", name, "SKILL.md"),
	}, nil
}

func installSkill(name, content, target string, force bool) ([]string, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	roots := map[string]string{
		"codex":  ".codex",
		"claude": ".claude",
		"agents": ".agents",
	}
	var selected []string
	if target == "all" {
		selected = []string{"codex", "claude", "agents"}
	} else if _, ok := roots[target]; ok {
		selected = []string{target}
	} else {
		return nil, errors.New("--target must be all, codex, claude, or agents")
	}
	paths, err := skillPaths(name)
	if err != nil {
		return nil, err
	}
	var installed []string
	for _, selectedTarget := range selected {
		path := paths[selectedTarget]
		if err := writeManagedSkillFile(path, content, force); err != nil {
			return nil, err
		}
		if metadata, ok := skillbundle.OpenAIYAML(name); ok && strings.TrimSpace(metadata) != "" {
			metadataPath := filepath.Join(filepath.Dir(path), "agents", "openai.yaml")
			if err := writeManagedSkillFile(metadataPath, metadata, force); err != nil {
				return nil, err
			}
		}
		installed = append(installed, path)
	}
	return installed, nil
}

func writeManagedSkillFile(path, content string, force bool) error {
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		if string(existing) == content {
			return nil
		}
		if !force {
			return fmt.Errorf("Skill already exists with different content: %s (use --force to replace it)", path)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".skill-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.WriteString(temp, content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func leadingArgument(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
