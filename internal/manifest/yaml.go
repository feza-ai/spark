package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseYAML parses YAML bytes into a nested map[string]interface{} structure.
func ParseYAML(data []byte) (map[string]interface{}, error) {
	lines := strings.Split(string(data), "\n")
	root := make(map[string]interface{})
	_, err := parseYAMLLines(lines, 0, 0, root)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func parseYAMLLines(lines []string, start, baseIndent int, m map[string]interface{}) (int, error) {
	i := start
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			i++
			continue
		}

		lineIndent := countIndent(line)
		if lineIndent < baseIndent {
			return i, nil
		}
		if lineIndent > baseIndent {
			return i, nil
		}

		if strings.HasPrefix(trimmed, "- ") {
			return i, nil
		}

		colonIdx := findMapSeparator(trimmed)
		if colonIdx < 0 {
			i++
			continue
		}

		key := strings.TrimSpace(trimmed[:colonIdx])
		rest := ""
		if colonIdx+1 < len(trimmed) {
			rest = strings.TrimSpace(trimmed[colonIdx+1:])
		}

		if rest != "" {
			// Flow-style list: ["a", "b", 1]
			if strings.HasPrefix(rest, "[") {
				list, err := parseFlowList(rest)
				if err != nil {
					return 0, fmt.Errorf("line %d: %w", i+1, err)
				}
				m[key] = list
				i++
				continue
			}
			// Block scalar indicators: | (literal), |- (strip), > (folded), >- (folded strip).
			if rest == "|" || rest == "|-" || rest == ">" || rest == ">-" {
				scalar, newIdx := parseBlockScalar(lines, i+1, baseIndent, rest)
				m[key] = scalar
				i = newIdx
				continue
			}
			m[key] = unquote(rest)
			i++
		} else {
			nextIdx := nextNonEmpty(lines, i+1)
			if nextIdx >= len(lines) {
				m[key] = ""
				i++
				continue
			}

			nextLine := lines[nextIdx]
			nextTrimmed := strings.TrimSpace(nextLine)
			nextIndent := countIndent(nextLine)

			if nextIndent <= baseIndent {
				m[key] = ""
				i++
			} else if strings.HasPrefix(nextTrimmed, "- ") {
				list, newIdx, err := parseYAMLList(lines, nextIdx, nextIndent)
				if err != nil {
					return 0, err
				}
				m[key] = list
				i = newIdx
			} else {
				child := make(map[string]interface{})
				newIdx, err := parseYAMLLines(lines, nextIdx, nextIndent, child)
				if err != nil {
					return 0, err
				}
				m[key] = child
				i = newIdx
			}
		}
	}
	return i, nil
}

func parseYAMLList(lines []string, start, baseIndent int) ([]interface{}, int, error) {
	var list []interface{}
	i := start

	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}

		lineIndent := countIndent(line)
		if lineIndent < baseIndent {
			return list, i, nil
		}
		if lineIndent > baseIndent {
			i++
			continue
		}

		if !strings.HasPrefix(trimmed, "- ") {
			return list, i, nil
		}

		itemContent := trimmed[2:]

		if colonIdx := findMapSeparator(itemContent); colonIdx >= 0 {
			itemMap := make(map[string]interface{})
			key := strings.TrimSpace(itemContent[:colonIdx])
			val := ""
			if colonIdx+1 < len(itemContent) {
				val = strings.TrimSpace(itemContent[colonIdx+1:])
			}

			if val != "" {
				itemMap[key] = unquote(val)
			} else {
				itemMap[key] = ""
			}

			itemIndent := baseIndent + 2
			nextIdx := nextNonEmpty(lines, i+1)
			if nextIdx < len(lines) && countIndent(lines[nextIdx]) >= itemIndent {
				newIdx, err := parseYAMLLines(lines, nextIdx, itemIndent, itemMap)
				if err != nil {
					return nil, 0, err
				}
				i = newIdx
			} else {
				i++
			}
			list = append(list, itemMap)
		} else {
			list = append(list, unquote(itemContent))
			i++
		}
	}
	return list, i, nil
}

// parseFlowList parses a YAML flow-style sequence like `[a, "b", 1]`.
// Nested flow lists or maps are not supported and return an error.
func parseFlowList(s string) ([]interface{}, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("malformed flow list: %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []interface{}{}, nil
	}
	var items []interface{}
	var buf strings.Builder
	inSingle, inDouble := false, false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
			buf.WriteByte(c)
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
			buf.WriteByte(c)
		case '[', '{':
			if !inSingle && !inDouble {
				return nil, fmt.Errorf("nested flow collections are not supported: %q", s)
			}
			buf.WriteByte(c)
		case ',':
			if !inSingle && !inDouble {
				items = append(items, unquote(strings.TrimSpace(buf.String())))
				buf.Reset()
				continue
			}
			buf.WriteByte(c)
		default:
			buf.WriteByte(c)
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in flow list: %q", s)
	}
	last := strings.TrimSpace(buf.String())
	if last != "" {
		items = append(items, unquote(last))
	}
	return items, nil
}

// parseBlockScalar consumes a YAML block scalar (|, |-, >, >-) starting
// at line `start`. Lines that are more indented than `parentIndent` are
// included; the common indent (the indent of the first non-empty content
// line) is stripped. Returns the joined string and the next line index.
//
//	|   literal — preserve newlines, keep one trailing newline.
//	|-  literal strip — preserve newlines, strip trailing newlines.
//	>   folded  — join lines with single spaces, keep one trailing newline.
//	>-  folded strip — join lines with single spaces, strip trailing newlines.
func parseBlockScalar(lines []string, start, parentIndent int, indicator string) (string, int) {
	folded := strings.HasPrefix(indicator, ">")
	strip := strings.HasSuffix(indicator, "-")

	// Find content indent: first non-empty line with indent > parentIndent.
	contentIndent := -1
	end := start
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		ind := countIndent(line)
		if ind <= parentIndent {
			break
		}
		if contentIndent < 0 {
			contentIndent = ind
		}
		if ind < contentIndent {
			break
		}
		end++
	}
	if contentIndent < 0 {
		return "", end
	}

	var collected []string
	for j := start; j < end; j++ {
		line := lines[j]
		if strings.TrimSpace(line) == "" {
			collected = append(collected, "")
			continue
		}
		// Strip exactly contentIndent spaces (after expanding tabs as
		// countIndent does); use min so we don't slice past length.
		stripN := contentIndent
		if stripN > len(line) {
			stripN = len(line)
		}
		// Walk the prefix until we've stripped contentIndent columns.
		col := 0
		k := 0
		for k < len(line) && col < contentIndent {
			if line[k] == ' ' {
				col++
				k++
			} else if line[k] == '\t' {
				col += 2
				k++
			} else {
				break
			}
		}
		collected = append(collected, line[k:])
	}

	var joined string
	if folded {
		// Fold: replace single newlines with space, keep blank lines.
		var b strings.Builder
		for i, ln := range collected {
			if i > 0 {
				prev := collected[i-1]
				if prev == "" || ln == "" {
					b.WriteString("\n")
				} else {
					b.WriteString(" ")
				}
			}
			b.WriteString(ln)
		}
		joined = b.String()
	} else {
		joined = strings.Join(collected, "\n")
	}

	if strip {
		joined = strings.TrimRight(joined, "\n")
	} else {
		joined = strings.TrimRight(joined, "\n") + "\n"
	}
	return joined, end
}

func countIndent(line string) int {
	n := 0
	for _, ch := range line {
		if ch == ' ' {
			n++
		} else if ch == '\t' {
			n += 2
		} else {
			break
		}
	}
	return n
}

func nextNonEmpty(lines []string, start int) int {
	for i := start; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t != "" && !strings.HasPrefix(t, "#") {
			return i
		}
	}
	return len(lines)
}

// findMapSeparator returns the index of the first ':' that acts as a YAML
// map separator in s (i.e., followed by a space or end-of-string), skipping
// colons inside single- or double-quoted segments. Returns -1 if none.
func findMapSeparator(s string) int {
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if inSingle || inDouble {
				continue
			}
			if i == len(s)-1 {
				return i
			}
			next := s[i+1]
			if next == ' ' || next == '\t' {
				return i
			}
		}
	}
	return -1
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func getString(m map[string]interface{}, keys ...string) string {
	val := traverse(m, keys...)
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func getInt(m map[string]interface{}, keys ...string) int {
	s := getString(m, keys...)
	if s == "" {
		return 0
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func getMap(m map[string]interface{}, keys ...string) map[string]interface{} {
	val := traverse(m, keys...)
	if val == nil {
		return nil
	}
	if child, ok := val.(map[string]interface{}); ok {
		return child
	}
	return nil
}

func getList(m map[string]interface{}, keys ...string) []interface{} {
	val := traverse(m, keys...)
	if val == nil {
		return nil
	}
	if list, ok := val.([]interface{}); ok {
		return list
	}
	return nil
}

func getStringMap(m map[string]interface{}, keys ...string) map[string]string {
	val := traverse(m, keys...)
	if val == nil {
		return nil
	}
	sub, ok := val.(map[string]interface{})
	if !ok {
		return nil
	}
	result := make(map[string]string, len(sub))
	for k, v := range sub {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

func traverse(m map[string]interface{}, keys ...string) interface{} {
	if len(keys) == 0 || m == nil {
		return nil
	}
	val, ok := m[keys[0]]
	if !ok {
		return nil
	}
	if len(keys) == 1 {
		return val
	}
	child, ok := val.(map[string]interface{})
	if !ok {
		return nil
	}
	return traverse(child, keys[1:]...)
}

// parseCPU converts a CPU quantity string to millicores.
func parseCPU(s string) int {
	if strings.HasSuffix(s, "m") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "m"))
		return v
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f * 1000)
}

// parseMemory converts a memory quantity string to megabytes.
func parseMemory(s string) int {
	if strings.HasSuffix(s, "Gi") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "Gi"))
		return v * 1024
	}
	if strings.HasSuffix(s, "Mi") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "Mi"))
		return v
	}
	if strings.HasSuffix(s, "Ki") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "Ki"))
		return v / 1024
	}
	if strings.HasSuffix(s, "G") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "G"))
		return v * 1000
	}
	if strings.HasSuffix(s, "M") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "M"))
		return v
	}
	if strings.HasSuffix(s, "K") {
		v, _ := strconv.Atoi(strings.TrimSuffix(s, "K"))
		return v / 1000
	}
	v, _ := strconv.Atoi(s)
	return v / (1024 * 1024)
}
