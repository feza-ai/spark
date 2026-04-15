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
