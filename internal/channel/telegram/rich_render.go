package telegram

import (
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/recally-io/dex/internal/channel"
)

type fragment struct{ text, open, close string }

var codeLanguage = regexp.MustCompile(`^[A-Za-z0-9_+-]{1,32}$`)

// Telegram counts decoded text characters and structural blocks, not HTML bytes
// or UTF-16 units. Reserve two characters when joining independently valid blocks.
const richCharacters = 32768
const richBlocks = 500
const richInlineDepth = 12 // Leaves room for a table, row, cell, and quotation.

type richBlock struct {
	html               string
	characters, blocks int
}

type richBuilder struct {
	parts              []channel.Part
	html               strings.Builder
	characters, blocks int
}

func (b *richBuilder) flush() {
	if b.html.Len() == 0 {
		return
	}
	b.parts = append(b.parts, channel.Part{Rich: &channel.RichMessage{Version: 1, HTML: b.html.String()}})
	b.html.Reset()
	b.characters = 0
	b.blocks = 0
}
func (b *richBuilder) add(block richBlock) {
	if b.characters+block.characters+2 > richCharacters || b.blocks+block.blocks > richBlocks {
		b.flush()
	}
	if b.html.Len() > 0 {
		b.html.WriteString("\n\n")
		b.characters += 2
	}
	b.html.WriteString(block.html)
	b.characters += block.characters
	b.blocks += block.blocks
}

// renderRich emits only text formatting. Markdown images stay linked alt text;
// files retain the native media outbox and cannot become remote media fetches.
func renderRich(markdown string) []channel.Part {
	source := []byte(markdown)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	var out richBuilder
	type node struct {
		ast.Node
		prefix string
		quote  bool
	}
	work := []node{{Node: doc}}
	for len(work) > 0 {
		item := work[len(work)-1]
		work = work[:len(work)-1]
		n := item.Node
		wrap := func(block richBlock) richBlock {
			if item.quote {
				block.html = "<blockquote>" + block.html + "</blockquote>"
				block.blocks++
			}
			return block
		}
		switch n := n.(type) {
		case *extast.Table:
			if item.prefix != "" {
				out.add(wrap(richBlock{html: "<p>" + html.EscapeString(item.prefix) + "</p>", characters: utf8.RuneCountInString(item.prefix), blocks: 1}))
			}
			for _, block := range richTable(n, source, item.prefix) {
				out.add(wrap(block))
			}
			continue
		case *ast.Paragraph, *ast.TextBlock, *ast.Heading:
			tokens := []fragment{{text: item.prefix}}
			tokens = append(tokens, richInline(n, source)...)
			if heading, ok := n.(*ast.Heading); ok {
				tag := "h" + strconv.Itoa(heading.Level)
				tokens = append([]fragment{{open: "<" + tag + ">", close: "</" + tag + ">"}}, tokens...)
				tokens = append(tokens, fragment{close: "</" + tag + ">"})
			}
			for _, block := range splitRichInline(tokens) {
				if _, heading := n.(*ast.Heading); !heading {
					block.html = "<p>" + block.html + "</p>"
				}
				out.add(wrap(block))
			}
			continue
		case *ast.CodeBlock, *ast.FencedCodeBlock:
			if item.prefix != "" {
				out.add(wrap(richBlock{html: "<p>" + html.EscapeString(item.prefix) + "</p>", characters: utf8.RuneCountInString(item.prefix), blocks: 1}))
			}
			start := "<pre><code>"
			if code, ok := n.(*ast.FencedCodeBlock); ok {
				language := string(code.Language(source))
				if codeLanguage.MatchString(language) {
					start = `<pre><code class="language-` + language + `">`
				}
			}
			tokens := []fragment{{open: start, close: "</code></pre>"}, {text: string(n.Lines().Value(source))}, {close: "</code></pre>"}}
			for _, block := range splitRichInline(tokens) {
				out.add(wrap(block))
			}
			continue
		case *ast.ThematicBreak:
			out.add(wrap(richBlock{html: "<hr/>", blocks: 1}))
			continue
		case *ast.HTMLBlock:
			var literal strings.Builder
			literal.Write(n.Lines().Value(source))
			if n.HasClosure() {
				literal.Write(n.ClosureLine.Value(source))
			}
			for _, block := range splitRichInline([]fragment{{text: item.prefix + literal.String()}}) {
				block.html = "<p>" + block.html + "</p>"
				out.add(wrap(block))
			}
			continue
		case *ast.ListItem:
			mark := "• "
			if list, ok := n.Parent().(*ast.List); ok && list.IsOrdered() {
				number := list.Start
				for p := n.PreviousSibling(); p != nil; p = p.PreviousSibling() {
					number++
				}
				mark = strconv.Itoa(number) + ". "
			}
			item.prefix += mark
		case *ast.Blockquote:
			if item.quote {
				item.prefix += "> "
			}
			item.quote = true
		}
		for child := n.LastChild(); child != nil; child = child.PreviousSibling() {
			work = append(work, node{child, item.prefix, item.quote})
		}
	}
	out.flush()
	if len(out.parts) == 0 && strings.TrimSpace(markdown) != "" {
		return []channel.Part{{Rich: &channel.RichMessage{Version: 1, HTML: "<p>" + html.EscapeString(markdown) + "</p>"}}}
	}
	return out.parts
}

// richInline walks iteratively. Deep formatting becomes plain nested content;
// it does not drop text or build tags beyond Telegram's nesting limit.
func richInline(root ast.Node, source []byte) []fragment {
	type visit struct {
		ast.Node
		close string
		depth int
	}
	var out []fragment
	work := []visit{{Node: root}}
	for len(work) > 0 {
		item := work[len(work)-1]
		work = work[:len(work)-1]
		if item.close != "" {
			out = append(out, fragment{close: item.close})
			continue
		}
		n := item.Node
		open, end := "", ""
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
			out = append(out, fragment{text: value})
			continue
		case *ast.String:
			out = append(out, fragment{text: string(n.Value)})
			continue
		case *ast.CodeSpan:
			open, end = "<code>", "</code>"
		case *ast.Emphasis:
			open, end = "<i>", "</i>"
			if n.Level == 2 {
				open, end = "<b>", "</b>"
			}
		case *extast.Strikethrough:
			open, end = "<s>", "</s>"
		case *extast.TaskCheckBox:
			mark := "☐ "
			if n.IsChecked {
				mark = "☑ "
			}
			out = append(out, fragment{text: mark})
			continue
		case *ast.Link:
			open, end = richLink(n.Destination)
		case *ast.Image:
			open, end = richLink(n.Destination)
			if n.FirstChild() == nil {
				out = append(out, fragment{text: "image: " + string(n.Destination)})
				continue
			}
		case *ast.AutoLink:
			open, end = richLink(n.URL(source))
			if item.depth >= richInlineDepth {
				open, end = "", ""
			}
			if open != "" {
				out = append(out, fragment{open: open, close: end})
			}
			out = append(out, fragment{text: string(n.Label(source))})
			if open != "" {
				out = append(out, fragment{close: end})
			}
			continue
		case *ast.RawHTML:
			for i := 0; i < n.Segments.Len(); i++ {
				segment := n.Segments.At(i)
				out = append(out, fragment{text: string(segment.Value(source))})
			}
			continue
		}
		depth := item.depth
		if open != "" && depth < richInlineDepth {
			out = append(out, fragment{open: open, close: end})
			work = append(work, visit{close: end})
			depth++
		}
		for child := n.LastChild(); child != nil; child = child.PreviousSibling() {
			work = append(work, visit{Node: child, depth: depth})
		}
	}
	return out
}
func richLink(destination []byte) (string, string) {
	u, err := url.Parse(string(destination))
	escaped := html.EscapeString(string(destination))
	if err == nil && len(escaped) <= 1024 && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "tg" || u.Scheme == "mailto") {
		return `<a href="` + escaped + `">`, "</a>"
	}
	return "", ""
}

func splitRichInline(tokens []fragment) []richBlock {
	var parts []richBlock
	var body strings.Builder
	var stack []fragment
	characters := 0
	closeTags := func() string {
		var tags strings.Builder
		for _, tag := range slices.Backward(stack) {
			tags.WriteString(tag.close)
		}
		return tags.String()
	}
	flush := func() {
		if characters == 0 {
			return
		}
		parts = append(parts, richBlock{body.String() + closeTags(), characters, 1})
		body.Reset()
		characters = 0
		for _, tag := range stack {
			body.WriteString(tag.open)
		}
	}
	for _, token := range tokens {
		switch {
		case token.open != "":
			body.WriteString(token.open)
			stack = append(stack, token)
		case token.close != "":
			body.WriteString(token.close)
			stack = stack[:len(stack)-1]
		default:
			for _, r := range token.text {
				if characters == richCharacters {
					flush()
				}
				body.WriteString(html.EscapeString(string(r)))
				characters++
			}
		}
	}
	flush()
	return parts
}

func richTable(table *extast.Table, source []byte, prefix string) []richBlock {
	type row struct {
		html       string
		characters int
	}
	var rows []row
	for n := table.FirstChild(); n != nil; n = n.NextSibling() {
		var b strings.Builder
		b.WriteString("<tr>")
		characters := 0
		column := 0
		for cell := n.FirstChild(); cell != nil; cell = cell.NextSibling() {
			tag := "td"
			if n == table.FirstChild() {
				tag = "th"
			}
			align := ""
			switch table.Alignments[column] {
			case extast.AlignRight:
				align = ` align="right"`
			case extast.AlignCenter:
				align = ` align="center"`
			case extast.AlignLeft, extast.AlignNone:
			}
			column++
			b.WriteString("<" + tag + align + ">")
			for _, token := range richInline(cell, source) {
				switch {
				case token.open != "":
					b.WriteString(token.open)
				case token.close != "":
					b.WriteString(token.close)
				default:
					b.WriteString(html.EscapeString(token.text))
					characters += utf8.RuneCountInString(token.text)
				}
			}
			b.WriteString("</" + tag + ">")
		}
		b.WriteString("</tr>")
		rows = append(rows, row{b.String(), characters})
	}
	// A row or header that cannot fit is rendered as labeled rich paragraphs.
	// This is a pre-request content transform, not an API-error method fallback.
	fallback := len(table.Alignments) > 20
	for i, row := range rows {
		size := row.characters
		if i > 0 {
			size += rows[0].characters
		}
		fallback = fallback || size > richCharacters
	}
	if fallback {
		var blocks []richBlock
		var labels [][]fragment
		for cell := table.FirstChild().FirstChild(); cell != nil; cell = cell.NextSibling() {
			label := richInline(cell, source)
			if len(label) == 0 {
				label = []fragment{{text: "Column " + strconv.Itoa(len(labels)+1)}}
			}
			labels = append(labels, label)
		}
		for n := table.FirstChild(); n != nil; n = n.NextSibling() {
			column := 0
			for cell := n.FirstChild(); cell != nil; cell = cell.NextSibling() {
				tokens := []fragment{{text: prefix}}
				if n != table.FirstChild() {
					tokens = append(tokens, labels[column]...)
					tokens = append(tokens, fragment{text: ": "})
				}
				tokens = append(tokens, richInline(cell, source)...)
				for _, block := range splitRichInline(tokens) {
					block.html = "<p>" + block.html + "</p>"
					blocks = append(blocks, block)
				}
				column++
			}
		}
		return blocks
	}
	var blocks []richBlock
	header := rows[0]
	body := header.html
	characters := header.characters
	count := 2 // The table and its header row.
	flush := func() {
		blocks = append(blocks, richBlock{"<table>" + body + "</table>", characters, count})
		body = header.html
		characters = header.characters
		count = 2
	}
	for _, row := range rows[1:] {
		if characters+row.characters > richCharacters || count+1 > richBlocks-1 {
			flush()
		}
		body += row.html
		characters += row.characters
		count++
	}
	flush()
	return blocks
}
