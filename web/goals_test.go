package web

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestGoalsNavigationAndAssets(t *testing.T) {
	for _, page := range []struct{ file, active string }{
		{"index.html", "/"}, {"goals.html", "/goals.html"}, {"settings.html", "/settings.html"},
	} {
		data, err := FS.ReadFile(page.file)
		if err != nil {
			t.Fatal(err)
		}
		_, nav, ok := strings.Cut(string(data), `<nav class="nav" aria-label="Main">`)
		if !ok {
			t.Fatalf("%s: main navigation missing", page.file)
		}
		nav, _, _ = strings.Cut(nav, "</nav>")
		links := regexp.MustCompile(`<a\b[^>]*>`).FindAllString(nav, -1)
		if len(links) != 3 {
			t.Fatalf("%s: want three nav links, got %v", page.file, links)
		}
		for i, link := range []struct{ path, name string }{
			{"/", "Overview"}, {"/goals.html", "Goals"}, {"/settings.html", "Configuration"},
		} {
			if !strings.Contains(links[i], `href="`+link.path+`"`) || !strings.Contains(links[i], `aria-label="`+link.name+`"`) {
				t.Errorf("%s: wrong order or missing accessible mobile name: %s", page.file, links[i])
			}
			active := strings.Contains(links[i], `class="active"`) && strings.Contains(links[i], `aria-current="page"`)
			if active != (page.active == link.path) {
				t.Errorf("%s: incorrect active link: %s", page.file, links[i])
			}
		}
		if !strings.Contains(nav, `href="/icons.svg#trending-up"`) {
			t.Errorf("%s: goals icon missing", page.file)
		}
	}
	for _, asset := range []string{"goals.html", "goals.js", "style.css", "icons.svg"} {
		if data, err := FS.ReadFile(asset); err != nil || len(data) == 0 {
			t.Errorf("embedded goals asset %s: %v", asset, err)
		}
	}
	html, _ := FS.ReadFile("goals.html")
	for _, text := range []string{`src="/goals.js"`, `href="/style.css"`, `aria-label="Calendar goals"`, `aria-live="polite"`, `role="status"`, `aria-busy="true"`, "Read-only", "Monday-start", "Refresh"} {
		if !strings.Contains(string(html), text) {
			t.Errorf("goals page missing %q", text)
		}
	}
	if strings.Contains(string(html), `src="/app.js"`) {
		t.Error("goals page must not initialize overview statistics")
	}
}

func TestGoalsExampleMatchesFile(t *testing.T) {
	data, err := os.ReadFile("../goals.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") && line != "" {
			lines = append(lines, line)
		}
	}
	html, _ := FS.ReadFile("goals.html")
	if !strings.Contains(string(html), strings.Join(lines, "\n")) {
		t.Fatal("goals page example differs from goals.example.yaml")
	}
}

func TestGoalsJavaScriptBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; run node --test goals_behavior_test.cjs separately")
	}
	// Let Go's test cache observe changes to the non-embedded Node test source.
	if _, err := os.ReadFile("goals_behavior_test.cjs"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--test", "goals_behavior_test.cjs")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("goals JavaScript behavior: %v\n%s", err, output)
	}
}
