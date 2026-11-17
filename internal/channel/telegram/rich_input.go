package telegram

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"strings"
	"unicode/utf8"
)

// richPlain retains text from the documented text-bearing fields only. It does
// not fetch embedded media or interpret a widget, URL, or rich text as a command.
// The iterative walk bounds both work and output for untrusted update JSON.
func richPlain(raw jsontext.Value) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "[Unreadable rich content]"
	}
	type node struct {
		value     any
		separator string
		depth     int
	}
	work := []node{{value: value}}
	var out strings.Builder
	characters := 0
	const limit = 65536
	for steps := 0; len(work) > 0; steps++ {
		if steps >= 20000 || characters >= limit {
			out.WriteString("\n[Rich content limit reached]")
			break
		}
		item := work[len(work)-1]
		work = work[:len(work)-1]
		if item.depth > 64 {
			out.WriteString("[Rich nesting limit reached]")
			break
		}
		switch v := item.value.(type) {
		case string:
			if utf8.RuneCountInString(v) > limit-characters {
				v = string([]rune(v)[:limit-characters])
			}
			out.WriteString(v)
			characters += utf8.RuneCountInString(v)
		case []any:
			// Schedule one element at a time, so a large array cannot expand
			// the work stack before the step budget is checked.
			if len(v) > 0 {
				if len(v) > 1 {
					work = append(work, node{v[1:], item.separator, item.depth})
					if item.separator != "" {
						work = append(work, node{value: item.separator})
					}
				}
				work = append(work, node{v[0], item.separator, item.depth + 1})
			}
		case map[string]any:
			keys := []string{"text", "summary", "label", "latex", "blocks", "items", "cells", "caption", "credit"}
			found := false
			for _, key := range slices.Backward(keys) {
				child, ok := v[key]
				if !ok {
					continue
				}
				if found {
					work = append(work, node{value: "\n"})
				}
				found = true
				separator := ""
				if key == "blocks" || key == "items" || key == "cells" {
					separator = "\n"
				}
				work = append(work, node{child, separator, item.depth + 1})
			}
			if !found {
				if kind, ok := v["type"].(string); ok {
					work = append(work, node{value: "[Rich " + kind + "]"})
				}
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func (m *message) body() string {
	if m.Text != "" {
		return m.Text
	}
	if len(m.Rich) > 0 {
		return "Rich message:\n" + richPlain(m.Rich)
	}
	return m.Caption
}
