package engine

import (
	"regexp"
	"strconv"
	"strings"
)

// Batch is one T-SQL batch separated by GO.
type Batch struct {
	Text      string
	StartLine int // 1-based line in the original script where the batch starts
	Repeat    int // GO n
}

var goLine = regexp.MustCompile(`(?i)^\s*GO(?:\s+(\d+))?\s*(?:--.*)?$`)

// SplitBatches splits a script on GO separators the way SSMS / sqlcmd do:
// GO must be alone on its line (optionally followed by a repeat count) and is
// ignored inside string literals, quoted identifiers and comments.
func SplitBatches(script string) []Batch {
	var (
		batches   []Batch
		cur       strings.Builder
		startLine = 1
		line      = 1
		inStr     byte // '\'', '"', '[' or 0
		blockDep  int  // nesting depth of /* */ comments
	)
	flush := func(repeat int, nextStart int) {
		text := cur.String()
		if strings.TrimSpace(text) != "" {
			batches = append(batches, Batch{Text: text, StartLine: startLine, Repeat: repeat})
		}
		cur.Reset()
		startLine = nextStart
	}

	lines := strings.SplitAfter(script, "\n")
	for _, ln := range lines {
		if inStr == 0 && blockDep == 0 {
			trimmed := strings.TrimRight(ln, "\r\n")
			if m := goLine.FindStringSubmatch(trimmed); m != nil {
				repeat := 1
				if m[1] != "" {
					if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
						repeat = n
					}
				}
				flush(repeat, line+1)
				line++
				continue
			}
		}
		// Scan the line to update lexical state.
		for i := 0; i < len(ln); i++ {
			ch := ln[i]
			switch {
			case blockDep > 0:
				if ch == '*' && i+1 < len(ln) && ln[i+1] == '/' {
					blockDep--
					i++
				} else if ch == '/' && i+1 < len(ln) && ln[i+1] == '*' {
					blockDep++
					i++
				}
			case inStr != 0:
				closer := inStr
				if inStr == '[' {
					closer = ']'
				}
				if ch == closer {
					if i+1 < len(ln) && ln[i+1] == closer { // escaped '' "" ]]
						i++
					} else {
						inStr = 0
					}
				}
			default:
				switch {
				case ch == '-' && i+1 < len(ln) && ln[i+1] == '-':
					i = len(ln) // rest of line is a comment
				case ch == '/' && i+1 < len(ln) && ln[i+1] == '*':
					blockDep++
					i++
				case ch == '\'' || ch == '"' || ch == '[':
					inStr = ch
				}
			}
		}
		cur.WriteString(ln)
		line++
	}
	flush(1, line)
	return batches
}
