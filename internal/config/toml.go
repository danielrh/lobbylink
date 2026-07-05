package config

// Minimal, strict TOML-subset parser covering exactly what the
// p2p-lobby config file needs: [tables], [[array tables]], and
// key = value where value is a basic string, integer, boolean, or a
// (possibly multi-line) array of basic strings. Comments (#) and blank
// lines are allowed. Anything outside this subset is a hard error so
// config typos never pass silently.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// tomlDoc is the parse result: top-level and [table] keys live in
// tables under their dotted section name ("" for top level); [[name]]
// entries accumulate in arrays.
type tomlDoc struct {
	tables map[string]map[string]any // section -> key -> string|int64|bool|[]string
	arrays map[string][]map[string]any
}

type tomlParser struct {
	lines []string
	line  int // 1-based, for errors
}

func parseTOML(src string) (*tomlDoc, error) {
	doc := &tomlDoc{
		tables: map[string]map[string]any{"": {}},
		arrays: map[string][]map[string]any{},
	}
	p := &tomlParser{lines: strings.Split(src, "\n")}

	section := ""                 // current [table] name
	var arrayEntry map[string]any // current [[array]] entry, nil if in a plain table

	for p.line = 1; p.line <= len(p.lines); p.line++ {
		raw := p.lines[p.line-1]
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "[["):
			if !strings.HasSuffix(line, "]]") {
				return nil, p.errf("malformed array-of-tables header %q", line)
			}
			name := strings.TrimSpace(line[2 : len(line)-2])
			if !validTOMLKey(name) {
				return nil, p.errf("invalid table name %q", name)
			}
			arrayEntry = map[string]any{}
			doc.arrays[name] = append(doc.arrays[name], arrayEntry)
			section = ""
		case strings.HasPrefix(line, "["):
			if !strings.HasSuffix(line, "]") {
				return nil, p.errf("malformed table header %q", line)
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if !validTOMLKey(name) {
				return nil, p.errf("invalid table name %q", name)
			}
			if _, dup := doc.tables[name]; dup {
				return nil, p.errf("duplicate table [%s]", name)
			}
			doc.tables[name] = map[string]any{}
			section = name
			arrayEntry = nil
		default:
			key, val, err := p.parseKeyValue(line)
			if err != nil {
				return nil, err
			}
			var dest map[string]any
			if arrayEntry != nil {
				dest = arrayEntry
			} else {
				dest = doc.tables[section]
			}
			if _, dup := dest[key]; dup {
				return nil, p.errf("duplicate key %q", key)
			}
			dest[key] = val
		}
	}
	return doc, nil
}

// parseKeyValue handles `key = value`; for arrays it may consume
// following lines until the closing bracket.
func (p *tomlParser) parseKeyValue(line string) (string, any, error) {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return "", nil, p.errf("expected key = value, got %q", line)
	}
	key := strings.TrimSpace(line[:eq])
	if !validTOMLKey(key) {
		return "", nil, p.errf("invalid key %q", key)
	}
	rest := strings.TrimSpace(line[eq+1:])
	if rest == "" {
		return "", nil, p.errf("missing value for key %q", key)
	}
	if strings.HasPrefix(rest, "[") {
		val, err := p.parseArray(rest)
		return key, val, err
	}
	val, err := p.parseScalar(rest)
	return key, val, err
}

func (p *tomlParser) parseScalar(s string) (any, error) {
	switch {
	case strings.HasPrefix(s, `"`):
		str, remain, err := parseBasicString(s)
		if err != nil {
			return nil, p.errf("%v", err)
		}
		if strings.TrimSpace(remain) != "" {
			return nil, p.errf("trailing content after string: %q", remain)
		}
		return str, nil
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	default:
		n, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 10, 64)
		if err != nil {
			return nil, p.errf("unsupported value %q (expected string, integer, boolean, or string array)", s)
		}
		return n, nil
	}
}

// parseArray parses a string array whose opening bracket is at the
// start of first; it consumes subsequent lines (advancing p.line) until
// the matching close bracket.
func (p *tomlParser) parseArray(first string) ([]string, error) {
	// Accumulate raw text until brackets balance outside of strings.
	var buf strings.Builder
	buf.WriteString(first)
	for !arrayClosed(buf.String()) {
		if p.line >= len(p.lines) {
			return nil, p.errf("unterminated array")
		}
		p.line++
		buf.WriteString(" ")
		buf.WriteString(strings.TrimSpace(stripComment(p.lines[p.line-1])))
	}
	body := strings.TrimSpace(buf.String())
	if strings.HasPrefix(body, "[") {
		body = body[1:]
	}
	closing := lastBracketOutsideString(body)
	if closing < 0 {
		return nil, p.errf("malformed array")
	}
	if strings.TrimSpace(body[closing+1:]) != "" {
		return nil, p.errf("trailing content after array")
	}
	body = body[:closing]

	out := []string{}
	rest := strings.TrimSpace(body)
	for rest != "" {
		if !strings.HasPrefix(rest, `"`) {
			return nil, p.errf("arrays may contain only strings, got %q", rest)
		}
		str, remain, err := parseBasicString(rest)
		if err != nil {
			return nil, p.errf("%v", err)
		}
		out = append(out, str)
		rest = strings.TrimSpace(remain)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, ",") {
			return nil, p.errf("expected comma between array elements near %q", rest)
		}
		rest = strings.TrimSpace(rest[1:])
	}
	return out, nil
}

// arrayClosed reports whether s contains a ']' outside any string,
// meaning the array literal is complete.
func arrayClosed(s string) bool { return lastBracketOutsideString(s) >= 0 }

func lastBracketOutsideString(s string) int {
	inStr := false
	last := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch c {
			case '\\':
				i++ // skip escaped char
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case ']':
			last = i
		}
	}
	return last
}

// stripComment removes a # comment that is not inside a string.
func stripComment(s string) string {
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch c {
			case '\\':
				i++
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '#':
			return s[:i]
		}
	}
	return s
}

// parseBasicString parses a leading double-quoted TOML basic string,
// returning the decoded value and the remainder of the input.
func parseBasicString(s string) (string, string, error) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", fmt.Errorf("expected string")
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("unterminated escape")
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'u', 'U':
				width := 4
				if s[i] == 'U' {
					width = 8
				}
				if i+width >= len(s) {
					return "", "", fmt.Errorf("truncated unicode escape")
				}
				n, err := strconv.ParseUint(s[i+1:i+1+width], 16, 32)
				if err != nil {
					return "", "", fmt.Errorf("bad unicode escape: %v", err)
				}
				r := rune(n)
				if utf16.IsSurrogate(r) || !utf8.ValidRune(r) {
					return "", "", fmt.Errorf("invalid unicode escape %#x", n)
				}
				b.WriteRune(r)
				i += width
			default:
				return "", "", fmt.Errorf("unsupported escape \\%c", s[i])
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", "", fmt.Errorf("unterminated string")
}

func validTOMLKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func (p *tomlParser) errf(format string, args ...any) error {
	return fmt.Errorf("config line %d: %s", p.line, fmt.Sprintf(format, args...))
}
