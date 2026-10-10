package bot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSmoke(t *testing.T) {
	if got := splitChunks("ab\ncd", 4000); len(got) != 1 {
		t.Fatalf("short text split: %v", got)
	}
	chunks := splitChunks(strings.Repeat("x", 9000)+"\n"+strings.Repeat("y", 100), 4000)
	total := 0
	for _, c := range chunks {
		if len(c) > 4000 {
			t.Fatalf("chunk too long: %d", len(c))
		}
		total += len(c)
	}
	if len(chunks) != 3 || total != 9101 {
		t.Fatalf("chunks: %d total %d", len(chunks), total)
	}
	// newline split: 4000 x's, then the newline lets the rest regroup
	chunks = splitChunks(strings.Repeat("x", 3900)+"\n"+strings.Repeat("y", 200), 4000)
	if len(chunks) != 2 || chunks[0] != strings.Repeat("x", 3900) ||
		chunks[1] != strings.Repeat("y", 200) {
		t.Fatalf("newline split: %d chunks", len(chunks))
	}
	if got := stripMention("@BitBot where are the funds? @bitbot", "bitbot"); got != "where are the funds?" {
		t.Fatalf("stripMention: %q", got)
	}
	if got := stripMention("no mention here", "bitbot"); got != "no mention here" {
		t.Fatalf("stripMention passthrough: %q", got)
	}
	ids := parseChatIDs(" 111, -100abc ,222,")
	if !ids[111] || !ids[222] || len(ids) != 2 {
		t.Fatalf("parseChatIDs: %v", ids)
	}
	for _, admin := range []bool{false, true} {
		tools := toolsFor(admin)
		if len(tools) == 0 {
			t.Fatal("no tools")
		}
		for _, tl := range tools {
			var v map[string]any
			if err := json.Unmarshal(tl.Function.Parameters, &v); err != nil {
				t.Fatalf("bad schema %s: %v", tl.Function.Name, err)
			}
		}
	}
	if n := len(toolsFor(false)); n >= len(toolsFor(true)) {
		t.Fatalf("admin tool count %d not greater than read %d", len(toolsFor(true)), n)
	}
	if helpText(true) == "" || helpText(false) == "" {
		t.Fatal("empty help")
	}
}
