package card

import (
	"bytes"
	"encoding/xml"
	"fmt"
	htmlstd "html"
	"io"
	"strconv"
	"strings"

	"github.com/wyatt915/treeblood"
)

const maxMathExpressionBytes = 8 * 1024

type mathReplacement struct {
	token    string
	source   string
	original string
	display  bool
}

// extractMath replaces supported Markdown math delimiters with inert tokens.
// Goldmark can then render the surrounding Markdown safely; the tokens are
// replaced with sanitized MathML afterwards. Backtick code spans and fences
// are copied as-is so examples such as `$x$` remain code.
func extractMath(markdown string) (string, []mathReplacement) {
	prefix := "HANDOFFMATHPLACEHOLDER"
	for strings.Contains(markdown, prefix) {
		prefix += "X"
	}

	var output strings.Builder
	replacements := make([]mathReplacement, 0)
	for index := 0; index < len(markdown); {
		if markdown[index] == '`' || markdown[index] == '~' {
			marker := markdown[index]
			run := countRun(markdown, index, marker)
			if marker == '`' || run >= 3 {
				delimiter := strings.Repeat(string(marker), run)
				if relative := strings.Index(markdown[index+run:], delimiter); relative >= 0 {
					end := index + run + relative + run
					output.WriteString(markdown[index:end])
					index = end
					continue
				}
			}
		}

		type delimiterPair struct {
			open, close string
			display     bool
		}
		var pair delimiterPair
		switch {
		case strings.HasPrefix(markdown[index:], "$$") && !isEscaped(markdown, index):
			pair = delimiterPair{open: "$$", close: "$$", display: true}
		case strings.HasPrefix(markdown[index:], `\[`) && !isEscaped(markdown, index):
			pair = delimiterPair{open: `\[`, close: `\]`, display: true}
		case strings.HasPrefix(markdown[index:], `\(`) && !isEscaped(markdown, index):
			pair = delimiterPair{open: `\(`, close: `\)`, display: false}
		case markdown[index] == '$' && !isEscaped(markdown, index) && validInlineMathOpen(markdown, index):
			pair = delimiterPair{open: "$", close: "$", display: false}
		}
		if pair.open != "" {
			start := index + len(pair.open)
			end := findMathClose(markdown, start, pair.close)
			if end >= 0 {
				source := strings.TrimSpace(markdown[start:end])
				if source != "" && len(source) <= maxMathExpressionBytes {
					token := prefix + strconv.Itoa(len(replacements)) + "END"
					replacements = append(replacements, mathReplacement{
						token: token, source: source,
						original: markdown[index : end+len(pair.close)], display: pair.display,
					})
					output.WriteString(token)
					index = end + len(pair.close)
					continue
				}
			}
		}

		output.WriteByte(markdown[index])
		index++
	}
	return output.String(), replacements
}

func countRun(value string, start int, character byte) int {
	end := start
	for end < len(value) && value[end] == character {
		end++
	}
	return end - start
}

func isEscaped(value string, index int) bool {
	backslashes := 0
	for index--; index >= 0 && value[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func validInlineMathOpen(value string, index int) bool {
	if strings.HasPrefix(value[index:], "$$") || index+1 >= len(value) {
		return false
	}
	next := value[index+1]
	return next != '$' && next != ' ' && next != '\t' && next != '\r' && next != '\n'
}

func findMathClose(value string, start int, delimiter string) int {
	for index := start; index+len(delimiter) <= len(value); index++ {
		if !strings.HasPrefix(value[index:], delimiter) || isEscaped(value, index) {
			continue
		}
		if delimiter == "$" {
			if strings.HasPrefix(value[index:], "$$") || index == start {
				continue
			}
			previous := value[index-1]
			if previous == ' ' || previous == '\t' || previous == '\r' || previous == '\n' {
				continue
			}
			if strings.Contains(value[start:index], "\n") {
				return -1
			}
		}
		return index
	}
	return -1
}

func applyMathReplacements(rendered string, replacements []mathReplacement) string {
	for _, replacement := range replacements {
		mathML, err := renderMathML(replacement.source, replacement.display)
		if err != nil {
			className := "math-source"
			if replacement.display {
				className += " math-source-display"
			}
			mathML = `<code class="` + className + `">` + htmlstd.EscapeString(replacement.original) + `</code>`
		}
		if replacement.display {
			rendered = strings.ReplaceAll(rendered, "<p>"+replacement.token+"</p>", mathML)
		}
		rendered = strings.ReplaceAll(rendered, replacement.token, mathML)
	}
	return rendered
}

func renderMathML(source string, display bool) (string, error) {
	if source == "" || len(source) > maxMathExpressionBytes {
		return "", fmt.Errorf("invalid math expression length")
	}
	for _, command := range []string{`\class`, `\def`, `\gdef`, `\newcommand`, `\renewcommand`} {
		if strings.Contains(source, command) {
			return "", fmt.Errorf("unsupported math command")
		}
	}

	var raw string
	var err error
	if display {
		raw, err = treeblood.DisplayStyle(source, nil)
	} else {
		raw, err = treeblood.InlineStyle(source, nil)
	}
	if err != nil {
		return "", err
	}
	safe, err := sanitizeMathML(raw)
	if err != nil {
		return "", err
	}
	if display {
		return `<div class="math-display">` + safe + `</div>`, nil
	}
	return `<span class="math-inline">` + safe + `</span>`, nil
}

var allowedMathElements = map[string]bool{
	"math": true, "semantics": true, "annotation": true, "mrow": true,
	"mi": true, "mn": true, "mo": true, "mtext": true, "mspace": true,
	"mfrac": true, "msqrt": true, "mroot": true, "mstyle": true,
	"merror": true, "mpadded": true, "mphantom": true, "mfenced": true,
	"menclose": true, "msub": true, "msup": true, "msubsup": true,
	"munder": true, "mover": true, "munderover": true, "mmultiscripts": true,
	"mprescripts": true, "none": true, "mtable": true, "mtr": true,
	"mlabeledtr": true, "mtd": true, "maligngroup": true, "malignmark": true,
}

var allowedMathAttributes = map[string]bool{
	"xmlns": true, "display": true, "displaystyle": true, "scriptlevel": true,
	"mathvariant": true, "mathsize": true, "mathcolor": true, "mathbackground": true,
	"linethickness": true, "bevelled": true, "numalign": true, "denomalign": true,
	"stretchy": true, "symmetric": true, "maxsize": true, "minsize": true,
	"largeop": true, "movablelimits": true, "accent": true, "accentunder": true,
	"form": true, "fence": true, "separator": true, "lspace": true, "rspace": true,
	"width": true, "height": true, "depth": true, "voffset": true,
	"rowalign": true, "columnalign": true, "rowspacing": true, "columnspacing": true,
	"rowlines": true, "columnlines": true, "frame": true, "framespacing": true,
	"equalrows": true, "equalcolumns": true, "side": true, "minlabelspacing": true,
	"notation": true, "encoding": true, "dir": true,
}

func sanitizeMathML(raw string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(raw))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	depth := 0
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(typed.Name.Local)
			if !allowedMathElements[name] || (!rootSeen && name != "math") {
				return "", fmt.Errorf("unsupported MathML element %q", name)
			}
			rootSeen = true
			depth++
			typed.Name = xml.Name{Local: name}
			attributes := make([]xml.Attr, 0, len(typed.Attr))
			for _, attribute := range typed.Attr {
				attributeName := strings.ToLower(attribute.Name.Local)
				if !allowedMathAttributes[attributeName] {
					continue
				}
				attribute.Name = xml.Name{Local: attributeName}
				attributes = append(attributes, attribute)
			}
			typed.Attr = attributes
			if err := encoder.EncodeToken(typed); err != nil {
				return "", err
			}
		case xml.EndElement:
			if depth == 0 {
				return "", fmt.Errorf("invalid MathML nesting")
			}
			depth--
			typed.Name = xml.Name{Local: strings.ToLower(typed.Name.Local)}
			if err := encoder.EncodeToken(typed); err != nil {
				return "", err
			}
		case xml.CharData:
			if err := encoder.EncodeToken(typed); err != nil {
				return "", err
			}
		}
	}
	if !rootSeen || depth != 0 {
		return "", fmt.Errorf("invalid MathML document")
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	return output.String(), nil
}
