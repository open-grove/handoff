package skills

import _ "embed"

const Version = "0.10.1"

type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Metadata    Metadata `json:"metadata"`
}

type Metadata struct {
	CLIHelp  string   `json:"cliHelp"`
	Requires Requires `json:"requires"`
}

type Requires struct {
	Bins []string `json:"bins"`
}

//go:embed handoff/SKILL.md
var handoffMarkdown string

//go:embed handoff/agents/openai.yaml
var handoffOpenAIYAML string

var handoff = Skill{
	Name:        "handoff",
	Description: "Package current work into a portable, immutable Handoff for another person or Agent to continue, or receive and manage an existing Handoff.",
	Version:     Version,
	Metadata: Metadata{
		CLIHelp:  "handoff --help; handoff schema <action>",
		Requires: Requires{Bins: []string{"handoff"}},
	},
}

func List() []Skill {
	return []Skill{handoff}
}

func Read(name string) (string, bool) {
	if name != handoff.Name {
		return "", false
	}
	return handoffMarkdown, true
}

func OpenAIYAML(name string) (string, bool) {
	if name != handoff.Name {
		return "", false
	}
	return handoffOpenAIYAML, true
}
