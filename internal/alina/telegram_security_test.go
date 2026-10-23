package alina

import (
	"strings"
	"testing"
)

func TestTelegramRejectsDangerousMarkdownLinks(t *testing.T) {
	for _, source := range []string{
		"[blocked](javascript:alert(1))",
		"[blocked](JaVaScRiPt:alert(1))",
		"[blocked](&#106;avascript:alert(1))",
		"[blocked](data:text/html,unsafe)",
		"![blocked](javascript:alert(1))",
	} {
		t.Run(source, func(t *testing.T) {
			chunks := telegramText(source + "\n\n[allowed](https://example.com)")
			var text strings.Builder
			links := 0
			for _, chunk := range chunks {
				text.WriteString(chunk.Text)
				for _, entity := range chunk.Entities {
					if entity.Type != "text_link" {
						continue
					}
					links++
					if entity.URL != "https://example.com" {
						t.Fatalf("unsafe link escaped Telegram projection: %q", entity.URL)
					}
				}
			}
			if links != 1 || !strings.Contains(text.String(), "blocked") || !strings.Contains(text.String(), "allowed") {
				t.Fatalf("lost text or a valid link: %q, links=%d", text.String(), links)
			}
		})
	}
}
