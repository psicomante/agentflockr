package opencode

import (
	"context"
	"strings"
	"testing"
)

func TestOpenCodePreviewUsesLatestVisibleText(t *testing.T) {
	o := Adapter{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if !strings.Contains(args[2], "type=assistant") || !strings.Contains(args[2], "order=desc") {
			t.Fatal("preview must request latest assistant messages")
		}
		return []byte(`{"data":[{"type":"assistant","content":[{"type":"reasoning","text":"private reasoning"},{"type":"tool","tool":"read"}]},{"type":"assistant","content":[{"type":"text","text":"Tests passed\u001b[31m."}]}]}`), nil
	}}
	text, err := o.Preview(context.Background(), "ses_a")
	if err != nil || text != "Tests passed." {
		t.Fatalf("bad preview: %q %v", text, err)
	}
}
