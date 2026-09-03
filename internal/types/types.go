package types

import "time"

const (
	ProtocolVersion          = 7
	ContextAttachmentVersion = 1
	RedactionVersion         = "best-effort-v1"
)

type Message struct {
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	At        time.Time `json:"at,omitempty"`
	Phase     string    `json:"-"`
	TurnID    string    `json:"-"`
	Owned     bool      `json:"-"`
	Completed bool      `json:"-"`
}

type ChildSessionRef struct {
	ID        string `json:"-"`
	AgentPath string `json:"-"`
}

type Repository struct {
	Root         string   `json:"root,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
}

type Context struct {
	Source             string    `json:"source"`
	SessionID          string    `json:"session_id,omitempty"`
	SessionPath        string    `json:"-"`
	Cursor             string    `json:"cursor,omitempty"`
	CWD                string    `json:"cwd,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	NativeCompactFound bool      `json:"native_compact_found,omitempty"`
	// FullSession is accepted for wire compatibility with pre-v4 clients.
	// Canonical context is always complete, so this flag no longer changes
	// extraction or sanitization.
	FullSession bool       `json:"full_session,omitempty"`
	Messages    []Message  `json:"messages"`
	Repo        Repository `json:"repository,omitempty"`

	ParentSessionID string            `json:"-"`
	ThreadSource    string            `json:"-"`
	AgentPath       string            `json:"-"`
	ChildSessions   []ChildSessionRef `json:"-"`
}

// ContextAttachment is the portable, sanitized readable conversation that may
// optionally be stored beside an immutable handoff. It deliberately excludes
// provider session paths, session IDs, cursors, tool results, and thinking.
type ContextAttachment struct {
	Version            int        `json:"version"`
	Source             SourceRef  `json:"source"`
	NativeSummary      string     `json:"native_summary,omitempty"`
	NativeCompactFound bool       `json:"native_compact_found,omitempty"`
	Messages           []Message  `json:"messages"`
	Repository         Repository `json:"repository,omitempty"`
	Redaction          string     `json:"redaction"`
}

type ContextInfo struct {
	Available      bool   `json:"available"`
	Version        int    `json:"version"`
	MessageCount   int    `json:"message_count"`
	CharacterCount int    `json:"character_count"`
	Redaction      string `json:"redaction"`
}

type ContextResponse struct {
	HandoffID string            `json:"handoff_id"`
	Context   ContextAttachment `json:"context"`
}

type Sections struct {
	Intent          string         `json:"intent,omitempty"`
	HumanBackground string         `json:"human_background,omitempty"`
	HumanStatus     string         `json:"human_status,omitempty"`
	HumanTodos      []string       `json:"human_todos,omitempty"`
	HumanSummary    string         `json:"human_summary,omitempty"`
	HumanSections   []HumanSection `json:"human_sections,omitempty"`
	// The fields below are accepted for protocol-v5 compatibility. New share
	// generators should integrate this material into HumanSections instead.
	KeyConclusions  []string `json:"key_conclusions,omitempty"`
	Reasoning       []string `json:"reasoning,omitempty"`
	Examples        []string `json:"examples,omitempty"`
	Corrections     []string `json:"corrections,omitempty"`
	RejectedOptions []string `json:"rejected_options,omitempty"`
	Context         string   `json:"context"`
	Decisions       []string `json:"decisions"`
	CurrentState    string   `json:"current_state"`
	ImportantFiles  []string `json:"important_files"`
	NextSteps       []string `json:"next_steps"`
	OpenQuestions   []string `json:"open_questions"`
}

type HumanSection struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type SourceRef struct {
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`
	Cursor    string    `json:"cursor,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// PublishRequest always contains the generated handoff contract and may
// explicitly include a sanitized, portable context attachment.
type PublishRequest struct {
	Goal              string             `json:"goal"`
	Source            SourceRef          `json:"source"`
	Sections          Sections           `json:"sections"`
	Generator         string             `json:"generator"`
	ContextAttachment *ContextAttachment `json:"context_attachment,omitempty"`
	// TTLSeconds is accepted for compatibility with older clients and ignored.
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

type Handoff struct {
	Version   int          `json:"version"`
	ID        string       `json:"id"`
	Title     string       `json:"title,omitempty"`
	Intent    string       `json:"intent,omitempty"`
	Goal      string       `json:"goal"`
	Source    SourceRef    `json:"source"`
	Markdown  string       `json:"markdown"`
	Generator string       `json:"generator"`
	Context   *ContextInfo `json:"context,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

type CreateResponse struct {
	Handoff     Handoff `json:"handoff"`
	ShareURL    string  `json:"share_url,omitempty"`
	MarkdownURL string  `json:"markdown_url,omitempty"`
	DeleteToken string  `json:"delete_token,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
