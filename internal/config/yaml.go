package config

import (
	"fmt"
	"strings"
)

// This file implements a deliberately minimal parser for the subset of
// YAML this project's configuration schema needs: nested mappings,
// sequences of scalars or mappings, double/single-quoted strings, and
// '#' comments. It is NOT a general-purpose YAML parser -- there is no
// external YAML library available because this project takes on zero
// external dependencies. If the config schema grows to need YAML
// features not handled here (anchors, multi-line strings, flow
// collections, etc.), extend this parser deliberately rather than
// reaching for a dependency.
//
// The parser works in two stages:
//  1. preprocess: turn raw source lines into a flat list of rawLine,
//     stripping comments/blank lines and recording each line's
//     indentation depth.
//  2. parseMapping / parseSequence / parseValue: a small recursive
//     descent parser over that flat list, producing a generic tree of
//     map[string]interface{}, []interface{}, and string values.
//
// Type-specific conversion (strings to int, duration, etc.) happens
// later in decode.go, once we know which field we're populating.

// rawLine is a single non-blank, non-comment-only line of YAML source,
// with its leading indentation already measured and stripped.
type rawLine struct {
	indent  int    // number of leading spaces
	content string // line content after indentation, comments, and trailing whitespace removed
	lineNo  int    // 1-based source line number, for error messages
}

// parseYAML parses the minimal YAML subset described above into a
// generic tree rooted at a mapping. An empty document yields an empty,
// non-nil map.
func parseYAML(data []byte) (map[string]interface{}, error) {
	lines, err := preprocess(data)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return map[string]interface{}{}, nil
	}
	if lines[0].indent != 0 {
		return nil, fmt.Errorf("line %d: top-level content must start at column 0", lines[0].lineNo)
	}
	m, _, err := parseMapping(lines, 0, 0)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// preprocess strips comments and blank lines from the raw source and
// records the indentation depth of each remaining line.
func preprocess(data []byte) ([]rawLine, error) {
	var lines []rawLine
	for i, raw := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		raw = strings.TrimRight(raw, "\r")
		noComment := stripComment(raw)
		trimmed := strings.TrimRight(noComment, " \t")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent, content, err := splitIndent(trimmed)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		lines = append(lines, rawLine{
			indent:  indent,
			content: strings.TrimRight(content, " \t"),
			lineNo:  lineNo,
		})
	}
	return lines, nil
}

// stripComment removes a trailing '#' comment from a line, ignoring any
// '#' that appears inside a single- or double-quoted string.
func stripComment(line string) string {
	inSingle, inDouble := false, false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

// splitIndent counts the leading spaces on a line and returns the
// indentation depth along with the remaining content. Tabs are
// rejected: mixing tabs and spaces for YAML indentation is a common
// source of subtle issues, so this parser disallows tabs entirely.
func splitIndent(line string) (int, string, error) {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	if n < len(line) && line[n] == '\t' {
		return 0, "", fmt.Errorf("tab characters are not allowed for indentation, use spaces")
	}
	return n, line[n:], nil
}

// isSeqItem reports whether a (already indent-stripped) line begins a
// YAML sequence item, i.e. starts with "-" followed by either nothing
// or a space.
func isSeqItem(content string) bool {
	return content == "-" || strings.HasPrefix(content, "- ")
}

// looksLikeMapEntry reports whether s looks like the start of a mapping
// entry ("key: value" or "key:"), as opposed to a bare scalar.
func looksLikeMapEntry(s string) bool {
	return strings.Contains(s, ":")
}

// splitKeyVal splits a mapping-entry line "key: value" or "key:" into
// its key and (possibly empty) inline value. The first colon in the
// line is always treated as the separator, since keys in this schema
// never themselves contain a colon.
func splitKeyVal(content string, lineNo int) (key string, val string, hasInline bool, err error) {
	idx := strings.Index(content, ":")
	if idx == -1 {
		return "", "", false, fmt.Errorf("line %d: expected a mapping entry (\"key: value\"), got %q", lineNo, content)
	}
	key = strings.TrimSpace(content[:idx])
	if key == "" {
		return "", "", false, fmt.Errorf("line %d: empty key", lineNo)
	}
	val = strings.TrimSpace(content[idx+1:])
	if val == "" {
		return key, "", false, nil
	}
	return key, val, true, nil
}

// parseValue dispatches to parseSequence or parseMapping depending on
// whether the line at lines[i] begins a sequence or a mapping.
func parseValue(lines []rawLine, i int, indent int) (interface{}, int, error) {
	if i >= len(lines) {
		return nil, i, nil
	}
	if isSeqItem(lines[i].content) {
		return parseSequence(lines, i, indent)
	}
	return parseMapping(lines, i, indent)
}

// parseMapping parses a run of consecutive lines at exactly the given
// indentation depth as "key: value" entries, until indentation drops
// below indent or the lines run out. Nested values (deeper indentation,
// or another mapping/sequence) are parsed recursively.
func parseMapping(lines []rawLine, i int, indent int) (map[string]interface{}, int, error) {
	m := map[string]interface{}{}
	for i < len(lines) && lines[i].indent == indent {
		content := lines[i].content
		if isSeqItem(content) {
			// A sequence item at this indentation means the caller
			// asked for a mapping but the data is a sequence; let the
			// caller surface that mismatch via the missing key.
			break
		}
		key, val, hasInline, err := splitKeyVal(content, lines[i].lineNo)
		if err != nil {
			return nil, i, err
		}
		if hasInline {
			m[key] = parseScalar(val)
			i++
			continue
		}
		// No inline value: the value is either a nested block on the
		// following more-indented lines, a sequence at the same
		// indent level, or null (key present, no value at all --
		// treated as an empty string).
		i++
		if i < len(lines) && lines[i].indent > indent {
			val, ni, err := parseValue(lines, i, lines[i].indent)
			if err != nil {
				return nil, i, err
			}
			m[key] = val
			i = ni
		} else if i < len(lines) && lines[i].indent == indent && isSeqItem(lines[i].content) {
			val, ni, err := parseSequence(lines, i, indent)
			if err != nil {
				return nil, i, err
			}
			m[key] = val
			i = ni
		} else {
			m[key] = ""
		}
	}
	return m, i, nil
}

// parseSequence parses a run of consecutive "- ..." lines at exactly
// the given indentation depth into a slice. Each item may be a bare
// scalar, or the start of a nested mapping continued on subsequent
// more-indented lines (the standard YAML "- key: value" idiom).
func parseSequence(lines []rawLine, i int, indent int) ([]interface{}, int, error) {
	var result []interface{}
	for i < len(lines) && lines[i].indent == indent && isSeqItem(lines[i].content) {
		content := lines[i].content
		rest := strings.TrimSpace(strings.TrimPrefix(content, "-"))
		// Items in a "- key: value" sequence are conventionally
		// continued on lines indented two spaces past the dash; we
		// adopt that same convention for nested keys.
		itemIndent := indent + 2

		if rest == "" {
			// Bare "-" with the value given as a nested block on
			// subsequent, more-indented lines.
			i++
			if i < len(lines) && lines[i].indent > indent {
				val, ni, err := parseValue(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				result = append(result, val)
				i = ni
			} else {
				result = append(result, nil)
			}
			continue
		}

		if looksLikeMapEntry(rest) {
			// Reconstruct this item as a standalone mapping: the
			// inline "key: value" becomes its first line, followed by
			// any subsequent lines indented at or past itemIndent.
			sub := []rawLine{{indent: itemIndent, content: rest, lineNo: lines[i].lineNo}}
			j := i + 1
			for j < len(lines) && lines[j].indent >= itemIndent {
				sub = append(sub, lines[j])
				j++
			}
			val, _, err := parseMapping(sub, 0, itemIndent)
			if err != nil {
				return nil, i, err
			}
			result = append(result, val)
			i = j
			continue
		}

		// Bare scalar item, e.g. "- tcp".
		result = append(result, parseScalar(rest))
		i++
	}
	return result, i, nil
}

// parseScalar strips matching surrounding quotes (single or double)
// from a scalar value, if present. Unquoted values are returned
// unchanged. All scalars are kept as strings in the generic tree;
// numeric/duration/bool conversion happens in decode.go where the
// target field's expected type is known.
func parseScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
