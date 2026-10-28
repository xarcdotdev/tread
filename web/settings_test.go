package web

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestConfigurationGoalsCard(t *testing.T) {
	data, err := FS.ReadFile("settings.html")
	if err != nil {
		t.Fatal(err)
	}
	_, grid, ok := strings.Cut(string(data), `<div class="settings-grid">`)
	if !ok {
		t.Fatal("configuration grid missing")
	}
	_, card, ok := strings.Cut(grid, `<section class="card panel wide" aria-labelledby="goals-config-title">`)
	if !ok {
		t.Fatal("goal configuration card missing from configuration grid")
	}
	card, _, ok = strings.Cut(card, `</section>`)
	if !ok {
		t.Fatal("goal configuration card closing tag missing")
	}
	for _, text := range []string{
		`class="panel-head"`, `class="panel-title"`, `href="/icons.svg#trending-up"`,
		`<h2 id="goals-config-title">Read-only goal configuration</h2>`,
		`Goals file: <code id="goals-path">Loading…</code>`,
		`id="goals-edit-help"`, "goals.example.yaml", "goals.yaml", "config.toml",
		`then <a href="/goals.html">Refresh</a>`,
		"Refresh also reloads the personal profile, timezone and speed calibration. No restart is needed.",
		"All periods and targets are optional.", "Targets must be positive:",
		`<pre aria-label="Example goals YAML">`,
		"Walking time excludes pauses.",
		"Average speed is distance divided by walking hours, not an average of levels.",
		"Steps need step length or height; calories need weight.",
		"Missing speed calibration can make these estimates unavailable.",
		"Values above target stay visible; bars stop at 100%.",
	} {
		if !strings.Contains(card, text) {
			t.Errorf("goal configuration card missing %q", text)
		}
	}
}

func TestConfigurationJavaScriptBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; run node --test settings_behavior_test.cjs separately")
	}
	// Let Go's test cache observe changes to the non-embedded Node test source.
	if _, err := os.ReadFile("settings_behavior_test.cjs"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--test", "settings_behavior_test.cjs")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configuration JavaScript behavior: %v\n%s", err, output)
	}
}

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
