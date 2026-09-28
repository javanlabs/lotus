package web

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/uyab/lotus/internal/httpx"
)

// Basecoat leaves the combobox box blank when the chosen option's value is
// empty ("Everyone", "Any status"), so the box would show nothing at all.
// Without an explicit Placeholder, lotus_select shows that option's label.
func TestLotusSelect_EmptyChoiceShowsItsLabel(t *testing.T) {
	r := httpx.NewRenderer(os.DirFS("../../web/templates"), true)
	render := func(data map[string]any) string {
		var out bytes.Buffer
		if err := r.RenderNamed(&out, "lotus_select", data); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	opts := []httpx.SelectOption{{Value: "", Label: "Everyone"}, {Value: "mine", Label: "Mine"}}
	if html := render(map[string]any{"Name": "who", "Value": "", "Options": opts}); !strings.Contains(html, `placeholder="Everyone"`) {
		t.Errorf("empty choice shows no label:\n%s", html)
	}
	if html := render(map[string]any{"Name": "who", "Value": "mine", "Options": opts}); strings.Contains(html, "placeholder=") {
		t.Errorf("a real choice got a placeholder:\n%s", html)
	}
	if html := render(map[string]any{"Name": "folder", "Value": "", "Placeholder": "Top level", "Options": opts}); !strings.Contains(html, `placeholder="Top level"`) || strings.Contains(html, `placeholder="Everyone"`) {
		t.Errorf("an explicit Placeholder is not kept:\n%s", html)
	}
}
