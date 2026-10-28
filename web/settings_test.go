package web

import (
	"strings"
	"testing"
)

func TestConfigurationHeaderCopy(t *testing.T) {
	data, err := FS.ReadFile("settings.html")
	if err != nil {
		t.Fatal(err)
	}
	_, content, ok := strings.Cut(string(data), `<header class="page-head">`)
	if !ok {
		t.Fatal("configuration header missing")
	}
	header, _, ok := strings.Cut(content, `</header>`)
	if !ok {
		t.Fatal("configuration header closing tag missing")
	}
	if !strings.Contains(header, `<h1>Configuration</h1>`) {
		t.Error("configuration title missing")
	}
	for _, text := range []string{
		`class="eyebrow"`,
		"<p>",
		"Make it your own",
		"Your configuration",
		"Personal measurements and the details behind your statistics.",
	} {
		if strings.Contains(header, text) {
			t.Errorf("configuration header still contains %q", text)
		}
	}
	if !strings.Contains(header, "Read from config file") {
		t.Error("configuration status chip missing")
	}
}
