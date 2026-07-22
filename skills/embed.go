package skills

import _ "embed"

const Version = "0.5.1"

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

var handoff = Skill{
	Name:        "handoff",
	Description: "Package current Agent context into a compact HANDOFF.md for another person, Agent, or session.",
	Version:     Version,
	Metadata: Metadata{
		CLIHelp:  "handoff --help; handoff schema <command>",
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
