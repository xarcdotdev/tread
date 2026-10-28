package web

import (
	"strings"
	"testing"
)

func TestDashboardCopyAndSessionIcon(t *testing.T) {
	assets := make(map[string]string)
	for _, name := range []string{"index.html", "app.js", "icons.svg"} {
		data, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		assets[name] = string(data)
	}

	for _, text := range []string{
		"A little movement, every day",
		"Small steps. A clearer picture of your progress.",
		"Your monthly walking rhythm",
		"Your daily walking rhythm",
		"chart-subtitle",
	} {
		for _, name := range []string{"index.html", "app.js"} {
			if strings.Contains(assets[name], text) {
				t.Errorf("%s still contains %q", name, text)
			}
		}
	}

	_, sessions, ok := strings.Cut(assets["index.html"], `<section class="card sessions">`)
	if !ok {
		t.Fatal("recent sessions section missing")
	}
	header, _, ok := strings.Cut(sessions, `<div class="table-wrap">`)
	if !ok {
		t.Fatal("recent sessions table missing")
	}
	if !strings.Contains(header, `<h2>Recent sessions</h2>`) ||
		!strings.Contains(header, `href="/icons.svg#clock"`) ||
		strings.Contains(header, `href="/icons.svg#footprints"`) {
		t.Error("recent sessions heading must use the clock icon")
	}
	if !strings.Contains(assets["icons.svg"], `<symbol id="clock"`) {
		t.Error("clock icon missing from sprite")
	}
	if strings.Count(assets["index.html"], `href="/icons.svg#footprints"`) != 1 ||
		!strings.Contains(assets["app.js"], `icon("footprints")`) {
		t.Error("steps metric and session row icons must remain unchanged")
	}
}
