package memory

import "testing"

func TestImportanceFromExplicitIntensity(t *testing.T) {
	cases := []struct {
		quote string
		want  float64
	}{
		{"Remember that this is very important.", 1.0},
		{"记住这是最关键的决策。", 1.0},
		{"一定要记住，这个绝不能忘。", 1.0},
		{"This is a critical requirement.", 1.0},
		{"This is an important constraint.", 0.8},
		{"Remember to always use Go.", 0.6},
		{"记住以后 Mindmory 的 MCP server 优先用 Go。", 0.8}, // 优先
		{"Remember that retention matters.", 0.6},   // matters
		{"Remember that reusable work products survive.", 0.4},
		{"我们刚刚讨论了 Go。", 0.4},
		{"这只是个小事的记录。", 0.2}, // 小事
		{"This is a minor detail, not important.", 0.2},
	}
	for _, item := range cases {
		if got := Importance(item.quote); got != item.want {
			t.Fatalf("Importance(%q)=%v want %v", item.quote, got, item.want)
		}
	}
}

func TestImportanceIsDeterministicAndBounded(t *testing.T) {
	quotes := []string{
		"记住这是最关键的决策。",
		"Remember to always use Go.",
		"我们刚刚讨论了 Go。",
		"这只是个小事。",
		"",
		"cAsE InSeNsItIvE: THIS IS VERY IMPORTANT",
	}
	for _, q := range quotes {
		first, second := Importance(q), Importance(q)
		if first != second {
			t.Fatalf("Importance(%q) not deterministic: %v vs %v", q, first, second)
		}
		if first < 0 || first > 1 {
			t.Fatalf("Importance(%q)=%v out of [0,1]", q, first)
		}
	}
}
