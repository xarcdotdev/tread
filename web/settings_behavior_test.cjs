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
function harness(overview = false, browserTimezone = "UTC", search = "", storage = {}) {
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
    location: { search },
    URLSearchParams,
    Date: class extends Date {
      constructor(...args) { super(...(args.length ? args : ["2026-06-15T00:30:00Z"])); }
    },
    Intl: {
      DateTimeFormat: class extends Intl.DateTimeFormat {
        constructor(locale, options) {
          super(locale, { ...options, timeZone: options?.timeZone ?? browserTimezone });
        }
      },
      NumberFormat: Intl.NumberFormat,
    },
    ResizeObserver: class { observe() {} },
    fetch: (url) => new Promise((resolve, reject) => pending.push({ url, resolve, reject })),
    localStorage: {
      getItem: (key) => storage[key] ?? null,
      setItem: (key, value) => { storage[key] = String(value); },
    },
  });
  for (const file of ["common.js", "app.js"])
    vm.runInContext(fs.readFileSync(path.join(__dirname, file), "utf8"), context);
  return { context, elements, pending, device, demo };
}
test("Overview uses hourly labels and offset-qualified interval tooltips for 1d", () => {
  const h = harness(true, Intl.DateTimeFormat().resolvedOptions().timeZone);
  vm.runInContext(`configuration = { timezone: "Europe/Berlin" }; report = { period: "1d" };`, h.context);
  const text = (key, long = false) => vm.runInContext(`bucketText(${JSON.stringify(key)}, false, ${long})`, h.context);
  assert.match(text("2026-10-25T00:30:00Z"), /02:30/);
  const first = text("2026-10-25T00:30:00Z", true);
  const second = text("2026-10-25T01:30:00Z", true);
  assert.notEqual(first, second);
  assert.match(first, /GMT\+2.*–.*GMT\+1/);
  assert.match(second, /GMT\+1/);
  vm.runInContext(`report = { period: "1w" };`, h.context);
  assert.equal(text("2026-10-25"), new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(new Date(2026, 9, 25)));
  assert.match(fs.readFileSync(path.join(__dirname, "index.html"), "utf8"), /data-period="1d" aria-pressed="true">24 hours/);
});

test("overview hides a notice dismissed on another page", () => {
  const h = harness(true, "UTC", "", { "tread.dismissed-notices": '["step-length-estimate"]' });
  vm.runInContext(
    `notices(["Steps use an approximate step length of 41.4% of height.", "Calibration warning"])`,
    h.context,
  );
  assert.doesNotMatch(h.elements.notices.innerHTML, /approximate step length/);
  assert.match(h.elements.notices.innerHTML, /Calibration warning/);
});
const settle = () => new Promise(setImmediate);
const response = (value) => ({ ok: true, json: async () => value });
function configuration(demo = false) {
  return {
    demo,
    age: 36,
    config: {
      personal: { weight_kg: 75, height_cm: 180, birthdate: "1990-06-15", gender: "male" },
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
  assert.match(h.elements.personal.innerHTML, /Birthdate/);
  assert.match(h.elements.personal.innerHTML, /1990-06-15/);
  assert.match(h.elements.personal.innerHTML, /36 years/);
  assert.match(h.elements.personal.innerHTML, /MET × personalized resting energy/);
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

for (const age of [null, undefined]) {
  test(`unavailable server age uses the unset and basic calorie fallbacks: ${age}`, async () => {
    const h = harness();
    const value = configuration();
    value.age = age;
    value.config.personal.age = 36; // A legacy value must not be used as a fallback.
    h.pending.shift().resolve(response(value));
    await settle();
    assert.match(h.elements.personal.innerHTML, /Age<\/span><span class="field-value unset">Not set/);
    assert.match(h.elements.personal.innerHTML, /1990-06-15/);
    assert.match(h.elements.personal.innerHTML, /MET × weight × hours/);
    assert.doesNotMatch(h.elements.personal.innerHTML, /personalized resting energy|36 years/);
  });
}

test("zero server age is displayed without enabling personalized calories", async () => {
  const h = harness();
  const value = configuration();
  value.age = 0;
  value.config.personal.birthdate = "2026-06-15";
  h.pending.shift().resolve(response(value));
  await settle();
  assert.match(h.elements.personal.innerHTML, /2026-06-15/);
  assert.match(h.elements.personal.innerHTML, /Age<\/span><span class="field-value">0 years/);
  assert.match(h.elements.personal.innerHTML, /MET × weight × hours/);
  assert.doesNotMatch(h.elements.personal.innerHTML, /personalized resting energy/);
});

for (const change of [
  { weight_kg: 0 }, { height_cm: 0 }, { gender: "" }, { gender: "other" },
]) {
  test(`incomplete calorie profile uses basic MET: ${JSON.stringify(change)}`, async () => {
    const h = harness();
    const value = configuration();
    Object.assign(value.config.personal, change);
    h.pending.shift().resolve(response(value));
    await settle();
    assert.match(h.elements.personal.innerHTML, /MET × weight × hours/);
    assert.doesNotMatch(h.elements.personal.innerHTML, /personalized resting energy/);
  });
}

for (const timezone of ["", "Local", "UTC"]) {
  for (const browserTimezone of ["America/Los_Angeles", "Pacific/Kiritimati"]) {
    test(`settings honors server age with timezone=${JSON.stringify(timezone)} and browser=${browserTimezone}`, async () => {
      const h = harness(false, browserTimezone);
      const value = configuration();
      value.config.timezone = timezone;
      value.age = 35;
      h.pending.shift().resolve(response(value));
      await settle();
      assert.match(h.elements.personal.innerHTML, /1990-06-15/);
      assert.match(h.elements.personal.innerHTML, /35 years/);
      assert.match(h.elements.personal.innerHTML, /MET × personalized resting energy/);
      assert.doesNotMatch(h.elements.personal.innerHTML, /36 years/);
      assert.equal(h.elements.notices.innerHTML, "");
      assert.equal(h.pending[0].url, "/api/goals");
    });
  }
}

for (const [search, period] of [
  ["", "1d"],
  ["?period=", "1d"],
  ["?period=invalid", "1d"],
  ...["all", "3y", "1y", "3m", "1m", "1w", "1d"].map((period) => [
    `?period=${period}`, period,
  ]),
]) {
  test(`overview loads ${period} statistics without requesting goals for ${JSON.stringify(search)}`, async () => {
    const h = harness(true, "UTC", search);
    h.pending.shift().resolve(response(configuration()));
    await settle();
    assert.equal(h.pending.length, 1);
    assert.equal(h.pending[0].url, `/api/stats?period=${period}`);
    assert.equal(h.elements.notices.innerHTML, "");
  });
}
