package metrics

import (
	"fmt"
	"sort"
	"strings"
)

// Render formats metric families in Prometheus text exposition format v0.0.4.
func Render(families []MetricFamily) []byte {
	var b strings.Builder
	for _, f := range families {
		fmt.Fprintf(&b, "# HELP %s %s\n", f.Name, f.Help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", f.Name, f.Type)
		for _, m := range f.Metrics {
			if len(m.Labels) == 0 {
				fmt.Fprintf(&b, "%s %g\n", f.Name, m.Value)
			} else {
				fmt.Fprintf(&b, "%s{%s} %g\n", f.Name, formatLabels(m.Labels), m.Value)
			}
		}
	}
	return []byte(b.String())
}

// formatLabels renders sorted label pairs as key="escaped_value" comma-separated.
func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=\"%s\"", k, escapeLabelValue(labels[k]))
	}
	return strings.Join(parts, ",")
}

// escapeLabelValue escapes backslash, double-quote, and newline per Prometheus spec.
func escapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}
