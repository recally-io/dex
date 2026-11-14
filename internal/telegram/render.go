package telegram

import (
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type fragment struct{ text, open, close string }

// render keeps block boundaries when possible. Each part is valid Telegram HTML,
// including when a code block is longer than one message.
// Old partial replies have a cursor but no frozen payload. Keep their original chunk boundaries.
func render(markdown string, enhanced bool) []string {
	source := []byte(markdown)
	parser := goldmark.New()
	if enhanced {
		parser = goldmark.New(goldmark.WithExtensions(extension.GFM))
	}
	doc := parser.Parser().Parse(text.NewReader(source))
	var parts []string
	for block := doc.FirstChild(); block != nil; block = block.NextSibling() {
		var tokens []fragment
		renderNode(block, source, &tokens, enhanced)
		chunks := splitHTML(tokens)
		for _, chunk := range chunks {
			if len(parts) > 0 && units(parts[len(parts)-1])+units(chunk)+2 <= maxMessageLen {
				parts[len(parts)-1] += "\n\n" + chunk
			} else {
				parts = append(parts, chunk)
			}
		}
	}
	return parts
}

func renderNode(n ast.Node, source []byte, out *[]fragment, enhanced bool) {
	var open, end string
	switch n := n.(type) {
	case *ast.Text:
		raw := n.Segment.Value(source)
		if !n.IsRaw() {
			raw = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(raw)))
		}
		value := string(raw)
		if n.SoftLineBreak() || n.HardLineBreak() {
			value += "\n"
		}
		*out = append(*out, fragment{text: value})
		return
	case *ast.String:
		*out = append(*out, fragment{text: string(n.Value)})
		return
	case *ast.CodeSpan:
		open, end = "<code>", "</code>"
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		start := "<pre><code>"
		if block, ok := n.(*ast.FencedCodeBlock); enhanced && ok {
			language := string(block.Language(source))
			if codeLanguage.MatchString(language) {
				start = `<pre><code class="language-` + language + `">`
			}
		}
		*out = append(*out, fragment{open: start, close: "</code></pre>"})
		*out = append(*out, fragment{text: string(n.Lines().Value(source))})
		*out = append(*out, fragment{close: "</code></pre>"})
		return
	case *ast.Emphasis:
		open, end = "<i>", "</i>"
		if n.Level == 2 {
			open, end = "<b>", "</b>"
		}
	case *ast.Heading:
		open, end = "<b>", "</b>"
	case *extast.Strikethrough:
		open, end = "<s>", "</s>"
	case *extast.TaskCheckBox:
		mark := "☐ "
		if n.IsChecked {
			mark = "☑ "
		}
		*out = append(*out, fragment{text: mark})
		return
	case *extast.Table:
		var rows []string
		for row := n.FirstChild(); row != nil; row = row.NextSibling() {
			var cells []string
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				var tokens []fragment
				renderNode(cell, source, &tokens, enhanced)
				var text strings.Builder
				for _, token := range tokens {
					text.WriteString(token.text)
				}
				cells = append(cells, strings.TrimSpace(text.String()))
			}
			rows = append(rows, strings.Join(cells, " | "))
		}
		*out = append(*out, fragment{open: "<pre>", close: "</pre>"}, fragment{text: strings.Join(rows, "\n")}, fragment{close: "</pre>"})
		return
	case *ast.Blockquote:
		if enhanced {
			nested := false
			for parent := n.Parent(); parent != nil; parent = parent.Parent() {
				if parent.Kind() == ast.KindBlockquote {
					nested = true
					break
				}
			}
			if nested {
				*out = append(*out, fragment{text: "> "})
			} else {
				open, end = "<blockquote>", "</blockquote>"
			}
		}
	case *ast.Image:
		if enhanced {
			u, err := url.Parse(string(n.Destination))
			if err == nil && (u.Scheme == "https" || u.Scheme == "http") && len(n.Destination) <= 1024 {
				open, end = `<a href="`+html.EscapeString(string(n.Destination))+`">`, "</a>"
			}
			if n.FirstChild() == nil {
				*out = append(*out, fragment{text: "image: " + string(n.Destination)})
				return
			}
		}
	case *ast.Link:
		u, err := url.Parse(string(n.Destination))
		if err == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "tg" || u.Scheme == "mailto") && len(n.Destination) <= 1024 {
			open, end = `<a href="`+html.EscapeString(string(n.Destination))+`">`, "</a>"
		}
	case *ast.AutoLink:
		*out = append(*out, fragment{text: string(n.Label(source))})
		return
	case *ast.RawHTML:
		for i := 0; i < n.Segments.Len(); i++ {
			segment := n.Segments.At(i)
			*out = append(*out, fragment{text: string(segment.Value(source))})
		}
		return
	case *ast.HTMLBlock:
		for i := 0; i < n.Lines().Len(); i++ {
			line := n.Lines().At(i)
			*out = append(*out, fragment{text: string(line.Value(source))})
		}
		if n.HasClosure() {
			*out = append(*out, fragment{text: string(n.ClosureLine.Value(source))})
		}
		return
	case *ast.ListItem:
		mark := "• "
		if enhanced {
			if list, ok := n.Parent().(*ast.List); ok && list.IsOrdered() {
				number := list.Start
				for previous := n.PreviousSibling(); previous != nil; previous = previous.PreviousSibling() {
					number++
				}
				mark = strconv.Itoa(number) + ". "
			}
			for parent := n.Parent(); parent != nil; parent = parent.Parent() {
				if parent.Kind() == ast.KindListItem {
					mark = "  " + mark
				}
			}
		}
		*out = append(*out, fragment{text: mark})
	}
	if open != "" {
		*out = append(*out, fragment{open: open, close: end})
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		renderNode(c, source, out, enhanced)
	}
	if open != "" {
		*out = append(*out, fragment{close: end})
	}
	switch n.(type) {
	case *ast.ListItem, *ast.Paragraph:
		*out = append(*out, fragment{text: "\n"})
	}
}

func units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func splitHTML(tokens []fragment) []string {
	var parts []string
	var body strings.Builder
	var stack []fragment
	length := 0
	closing := func() string {
		var s strings.Builder
		for _, tag := range slices.Backward(stack) {
			s.WriteString(tag.close)
		}
		return s.String()
	}
	flush := func() {
		if body.Len() == 0 {
			return
		}
		parts = append(parts, body.String()+closing())
		body.Reset()
		length = 0
		for _, tag := range stack {
			body.WriteString(tag.open)
			length += units(tag.open)
		}
	}
	for _, token := range tokens {
		switch {
		case token.open != "":
			if length+units(token.open)+units(token.close)+units(closing()) > maxMessageLen {
				flush()
			}
			body.WriteString(token.open)
			length += units(token.open)
			stack = append(stack, token)
		case token.close != "":
			body.WriteString(token.close)
			length += units(token.close)
			stack = stack[:len(stack)-1]
		default:
			for _, r := range token.text {
				escaped := html.EscapeString(string(r))
				size := units(escaped)
				if length+size+units(closing()) > maxMessageLen {
					flush()
				}
				body.WriteString(escaped)
				length += size
			}
		}
	}
	if strings.TrimSpace(body.String()) != "" {
		parts = append(parts, strings.TrimSpace(body.String()))
	}
	return parts
}

var (
	htmlTags     = regexp.MustCompile(`<[^>]*>`)
	codeLanguage = regexp.MustCompile(`^[A-Za-z0-9_+-]{1,32}$`)
)
