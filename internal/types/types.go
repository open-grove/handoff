package types

import "time"

const ProtocolVersion = 1

type Message struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at,omitempty"`
}

type Repository struct {
	Root         string   `json:"root,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
}

type Context struct {
	Source    string     `json:"source"`
	SessionID string     `json:"session_id,omitempty"`
	Cursor    string     `json:"cursor,omitempty"`
	CWD       string     `json:"cwd,omitempty"`
	UpdatedAt time.Time  `json:"updated_at,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Messages  []Message  `json:"messages"`
	Repo      Repository `json:"repository,omitempty"`
}

type CreateRequest struct {
	Goal       string  `json:"goal"`
	Context    Context `json:"context"`
	TTLSeconds int64   `json:"ttl_seconds,omitempty"`
}

type SourceRef struct {
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`
	Cursor    string    `json:"cursor,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Handoff struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Goal      string    `json:"goal"`
	Source    SourceRef `json:"source"`
	Markdown  string    `json:"markdown"`
	Generator string    `json:"generator"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CreateResponse struct {
	Handoff  Handoff `json:"handoff"`
	ShareURL string  `json:"share_url,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
