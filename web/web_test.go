package web

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFavicon(t *testing.T) {
	index, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `<link rel="icon" type="image/svg+xml" href="/favicon.svg" />`) {
		t.Fatal("dashboard must link to the SVG favicon")
	}

	icon, err := FS.ReadFile("favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	var svg struct {
		XMLName xml.Name
		ViewBox string `xml:"viewBox,attr"`
	}
	if err := xml.Unmarshal(icon, &svg); err != nil {
		t.Fatalf("invalid favicon SVG: %v", err)
	}
	if svg.XMLName.Local != "svg" || svg.XMLName.Space != "http://www.w3.org/2000/svg" || svg.ViewBox != "0 0 64 64" {
		t.Fatalf("unexpected SVG root: %+v", svg)
	}

	// The server serves GET / through this embedded file handler.
	recorder := httptest.NewRecorder()
	http.FileServerFS(FS).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("favicon status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("favicon Content-Type = %q, want image/svg+xml", got)
	}
	if recorder.Body.String() != string(icon) {
		t.Error("favicon response does not match embedded SVG")
	}
}

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
