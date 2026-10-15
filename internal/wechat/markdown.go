package wechat

import "strings"

// filterMarkdown applies the official final-text rules. Fenced and inline code stay intact.
func filterMarkdown(text string) string {
	var out strings.Builder
	fence := false
	for line := range strings.SplitAfterSeq(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
			out.WriteString(line)
			continue
		}
		if fence {
			out.WriteString(line)
			continue
		}
		if rest, ok := strings.CutPrefix(line, "###### "); ok {
			line = rest
		} else if rest, ok := strings.CutPrefix(line, "##### "); ok {
			line = rest
		}
		// Horizontal rules are supported, including spaced forms.
		compact := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
		if len(compact) >= 3 && (strings.Trim(compact, "*") == "" || strings.Trim(compact, "_") == "" || strings.Trim(compact, "-") == "") {
			out.WriteString(line)
			continue
		}
		out.WriteString(filterInline(line))
	}
	return out.String()
}

func filterInline(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		if text[0] == '`' {
			n := len(text) - len(strings.TrimLeft(text, "`"))
			marker := text[:n]
			if end := strings.Index(text[n:], marker); end >= 0 {
				end += 2 * n
				out.WriteString(text[:end])
				text = text[end:]
				continue
			}
		}
		if strings.HasPrefix(text, "![") {
			if bracket := strings.Index(text, "]("); bracket >= 0 {
				if end := strings.IndexByte(text[bracket+2:], ')'); end >= 0 {
					text = text[bracket+3+end:]
					continue
				}
			}
		}
		marker := ""
		switch {
		case strings.HasPrefix(text, "***"):
			marker = "***"
		case strings.HasPrefix(text, "___"):
			marker = "___"
		case strings.HasPrefix(text, "**"), strings.HasPrefix(text, "__"):
			out.WriteString(text[:2])
			text = text[2:]
			continue
		case len(text) > 1 && (text[0] == '*' || text[0] == '_') && text[1] != ' ' && text[1] != '\n':
			marker = text[:1]
		}
		if marker != "" {
			if end := closingMarker(text[len(marker):], marker); end >= 0 {
				content := text[len(marker) : len(marker)+end]
				if containsCJK(content) {
					out.WriteString(content)
				} else {
					out.WriteString(marker)
					out.WriteString(content)
					out.WriteString(marker)
				}
				text = text[end+2*len(marker):]
				continue
			}
		}
		out.WriteByte(text[0])
		text = text[1:]
	}
	return out.String()
}

func closingMarker(text, marker string) int {
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' && len(marker) == 1 {
			return -1
		}
		if strings.HasPrefix(text[i:], marker) {
			if len(marker) == 1 && i+1 < len(text) && text[i+1] == marker[0] {
				i++
				continue
			}
			return i
		}
	}
	return -1
}

func containsCJK(text string) bool {
	return strings.ContainsFunc(text, func(r rune) bool {
		return r >= 0x2e80 && r <= 0x9fff || r >= 0xac00 && r <= 0xd7af || r >= 0xf900 && r <= 0xfaff
	})
}
