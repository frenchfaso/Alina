package alina

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/yuin/goldmark"
	"golang.org/x/net/html"
)

type tgEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
}
type tgText struct {
	Text     string
	Entities []tgEntity
}

// Parse CommonMark once, then project only Telegram-supported styles. Using
// entities avoids HTML escaping and lets each chunk retain valid local spans.
func telegramText(markdown string) []tgText {
	var rendered bytes.Buffer
	if err := goldmark.Convert([]byte(markdown), &rendered); err != nil {
		return splitTelegramText(markdown, nil)
	}
	doc, err := html.Parse(&rendered)
	if err != nil {
		return splitTelegramText(markdown, nil)
	}
	var out strings.Builder
	var spans []tgEntity
	units := 0
	write := func(s string) { out.WriteString(s); units += len(utf16.Encode([]rune(s))) }
	newline := func() {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			write("\n")
		}
	}
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inPre bool) {
		if n.Type == html.TextNode {
			write(n.Data)
			return
		}
		if n.Type != html.ElementNode && n.Type != html.DocumentNode {
			return
		}
		kind, url := "", ""
		switch n.Data {
		case "strong", "b":
			kind = "bold"
		case "em", "i":
			kind = "italic"
		case "code":
			if !inPre {
				kind = "code"
			}
		case "pre":
			newline()
			kind = "pre"
			inPre = true
		case "a":
			for _, a := range n.Attr {
				if a.Key == "href" && (strings.HasPrefix(a.Val, "https://") || strings.HasPrefix(a.Val, "http://") || strings.HasPrefix(a.Val, "mailto:")) {
					kind, url = "text_link", a.Val
				}
			}
		case "img":
			for _, a := range n.Attr {
				if a.Key == "alt" {
					write(a.Val)
				}
			}
			return
		case "br":
			write("\n")
			return
		case "li":
			newline()
			if n.Parent != nil && n.Parent.Data == "ol" {
				index := 1
				for s := n.PrevSibling; s != nil; s = s.PrevSibling {
					if s.Type == html.ElementNode && s.Data == "li" {
						index++
					}
				}
				write(strconv.Itoa(index) + ". ")
			} else {
				write("• ")
			}
		case "p", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6":
			newline()
		}
		start := units
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inPre)
		}
		if kind != "" && units > start {
			spans = append(spans, tgEntity{Type: kind, Offset: start, Length: units - start, URL: url})
		}
		switch n.Data {
		case "p", "pre", "li", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6":
			newline()
		}
	}
	walk(doc, false)
	// Telegram does not allow code/pre entities inside other formatting.
	valid := make([]tgEntity, 0, len(spans))
	for _, span := range spans {
		overlapsCode := false
		if span.Type != "code" && span.Type != "pre" {
			for _, other := range spans {
				if (other.Type == "code" || other.Type == "pre") && span.Offset < other.Offset+other.Length && other.Offset < span.Offset+span.Length {
					overlapsCode = true
					break
				}
			}
		}
		if !overlapsCode {
			valid = append(valid, span)
		}
	}
	return splitTelegramText(strings.TrimRight(out.String(), "\n"), valid)
}

func splitTelegramText(text string, spans []tgEntity) []tgText {
	var result []tgText
	runes := []rune(text)
	offset := 0
	for len(runes) > 0 {
		count, units, boundary := 0, 0, 0
		for count < len(runes) {
			w := 1
			if runes[count] > 0xffff {
				w = 2
			}
			if units+w > 3500 {
				break
			}
			units += w
			count++
			if runes[count-1] == '\n' {
				boundary = count
			}
		}
		if count < len(runes) && boundary > count/2 {
			count = boundary
		}
		part := string(runes[:count])
		units = len(utf16.Encode(runes[:count]))
		chunk := tgText{Text: part}
		for _, s := range spans {
			start, end := max(offset, s.Offset), min(offset+units, s.Offset+s.Length)
			if start < end {
				s.Offset, s.Length = start-offset, end-start
				chunk.Entities = append(chunk.Entities, s)
			}
		}
		if strings.TrimSpace(part) != "" {
			result = append(result, chunk)
		}
		offset += units
		runes = runes[count:]
	}
	return result
}
