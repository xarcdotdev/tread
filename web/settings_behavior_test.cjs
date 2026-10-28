"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor() {
    this.textContent = "";
    this.innerHTML = "";
    this.classList = new Set();
    this.classList.remove = this.classList.delete;
  }
}
function harness(overview = false) {
  const ids = overview
    ? ["notices", "today", "refresh", "activity-chart"]
    : ["notices", "personal", "device", "application", "calibration-state", "calibration", "goals-path"];
  const elements = Object.fromEntries(ids.map((id) => [id, new Element()]));
  if (!overview) {
    elements["goals-path"].textContent = "Loading…";
    Object.defineProperty(elements["goals-path"], "innerHTML", {
      set() { throw new Error("Goals path must use textContent"); },
    });
  }
  const device = new Element();
  const demo = new Element();
  const pending = [];
  const context = vm.createContext({
    document: {
      getElementById: (id) => elements[id] || null,
      querySelectorAll: (selector) => selector === "[data-device]" ? [device]
        : selector === "[data-demo]" ? [demo] : [],
      body: new Element(),
    },
    location: { search: "" },
    URLSearchParams,
    ResizeObserver: class { observe() {} },
    fetch: (url) => new Promise((resolve, reject) => pending.push({ url, resolve, reject })),
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, "app.js"), "utf8"), context);
  return { context, elements, pending, device, demo };
}
const settle = () => new Promise(setImmediate);
const response = (value) => ({ ok: true, json: async () => value });
function configuration(demo = false) {
  return {
    demo,
    config: {
      personal: { weight_kg: 75, height_cm: 180, age: 30, gender: "male" },
      treadmill: {
        name: "Test treadmill", driver: "mock", initial_level: 1,
        calibrated: true, speed_kmh: { 1: 1.5, 2: 2 },
      },
      path: "/server/config.toml", db_path: "/server/walking.db",
      device: "test-device", listen: "127.0.0.1:8080", timezone: "UTC",
    },
  };
}
function assertSettings(h) {
  assert.match(h.elements.personal.innerHTML, /75 kg/);
  assert.match(h.elements.device.innerHTML, /Test treadmill/);
  assert.match(h.elements.application.innerHTML, /\/server\/config.toml/);
  assert.match(h.elements.calibration.innerHTML, /1.50 km\/h/);
  assert.match(h.elements["calibration-state"].innerHTML, /Calibration confirmed|demo only/);
  assert.equal(h.device.textContent, "Test treadmill");
}

for (const [label, demo, goalsPath] of [
  ["server", false, "/custom/<img onerror=bad>/goals.yaml"],
  ["demo", true, "Built-in sample targets (no file)"],
]) {
  test(`Configuration displays the live ${label} goals path as text`, async () => {
    const h = harness();
    assert.equal(h.pending[0].url, "/api/config");
    h.pending.shift().resolve(response(configuration(demo)));
    await settle();
    assertSettings(h); // Other configuration renders before the goals request finishes.
    assert.equal(h.elements["goals-path"].textContent, "Loading…");
    assert.equal(h.pending[0].url, "/api/goals");
    h.pending.shift().resolve(response({ path: goalsPath }));
    await settle();
    assert.equal(h.elements["goals-path"].textContent, goalsPath);
    assertSettings(h);
    assert.equal(h.demo.hidden, !demo);
    assert.equal(h.pending.length, 0);
    if (demo) assert.match(h.elements.notices.innerHTML, /Demo profile/);
    else assert.equal(h.elements.notices.innerHTML, "");
  });
}

for (const failure of ["HTTP", "network", "JSON"]) {
  test(`a goals ${failure} failure leaves configuration and demo notices intact`, async () => {
    const h = harness();
    h.pending.shift().resolve(response(configuration(true)));
    await settle();
    const notices = h.elements.notices.innerHTML;
    assert.match(notices, /Demo profile/);
    const request = h.pending.shift();
    assert.equal(request.url, "/api/goals");
    if (failure === "HTTP") request.resolve({ ok: false, text: async () => "goals.yaml: invalid <YAML>" });
    else if (failure === "network") request.reject(new Error("Network unavailable"));
    else request.resolve({ ok: true, json: async () => { throw new Error("Invalid JSON"); } });
    await settle();
    assert.match(h.elements["goals-path"].textContent, /^Unavailable: /);
    assert.doesNotMatch(h.elements["goals-path"].textContent, /Loading/);
    if (failure === "HTTP") assert.match(h.elements["goals-path"].textContent, /invalid <YAML>/);
    assertSettings(h);
    assert.equal(h.elements.notices.innerHTML, notices);
    assert.equal(h.demo.hidden, false);
    assert.equal(h.pending.length, 0);
  });
}

test("configuration failures retain their own error without requesting goals", async () => {
  const h = harness();
  h.pending.shift().resolve({ ok: false, text: async () => "invalid TOML" });
  await settle();
  assert.match(h.elements.notices.innerHTML, /Could not load configuration: invalid TOML/);
  assert.equal(h.pending.length, 0);
});

test("overview keeps loading statistics without requesting goals", async () => {
  const h = harness(true);
  h.pending.shift().resolve(response(configuration()));
  await settle();
  assert.equal(h.pending.length, 1);
  assert.equal(h.pending[0].url, "/api/stats?period=1m");
  assert.equal(h.elements.notices.innerHTML, "");
});
