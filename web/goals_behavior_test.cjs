"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

// Only the DOM methods this page uses. innerHTML deliberately fails the test.
class Element {
  constructor(tag = "div") {
    this.tagName = tag;
    this.children = [];
    this.attributes = {};
    this.dataset = {};
    this.classList = new Set();
    this.classList.remove = this.classList.delete;
    this.style = { setProperty: (key, value) => { this.style[key] = value; } };
    this._text = "";
  }
  set textContent(text) { this._text = String(text); this.children = []; }
  get textContent() { return this._text + this.children.map((el) => el.textContent).join(""); }
  set innerHTML(_) { throw new Error("Use textContent, not HTML interpolation"); }
  append(...elements) { this.adopt(elements); this.children.push(...elements); }
  replaceChildren(...elements) { this._text = ""; this.adopt(elements); this.children = elements; }
  adopt(elements) { for (const el of elements) if (el instanceof Element) el.parent = this; }
  remove() { this.parent.children = this.parent.children.filter((el) => el !== this); }
  setAttribute(key, value) { this.attributes[key] = String(value); }
}
function descendants(element, tag) {
  return element.children.flatMap((child) => [
    ...(child.tagName === tag ? [child] : []),
    ...descendants(child, tag),
  ]);
}
function harness(storage = {}) {
  const elements = Object.fromEntries([
    "refresh", "goals-periods", "goals-status", "goals-calendar", "notices",
  ].map((id) => [id, new Element()]));
  const device = new Element();
  const demo = new Element();
  const pending = [];
  const context = vm.createContext({
    document: {
      getElementById: (id) => elements[id],
      createElement: (tag) => new Element(tag),
      querySelectorAll: (selector) => selector === "[data-demo]" ? [demo] : [device],
      body: new Element("body"),
    },
    fetch: (url, options) => new Promise((resolve, reject) => pending.push({ url, options, resolve, reject })),
    localStorage: {
      getItem: (key) => storage[key] ?? null,
      setItem: (key, value) => { storage[key] = String(value); },
    },
  });
  for (const file of ["notices.js", "goals.js"])
    vm.runInContext(fs.readFileSync(path.join(__dirname, file), "utf8"), context);
  return { context, elements, pending, device, demo };
}
const settle = () => new Promise(setImmediate);
function report() {
  return {
    path: "/config/<img onerror=bad>/goals.yaml",
    timezone: "UTC", device: "<script>not HTML</script>", demo: false,
    periods: [
      { id: "daily", label: "Daily", from_date: "2025-03-05", through_date: "2025-03-05", from: "2025-03-05T00:00:00Z", to: "2025-03-06T00:00:00Z", warnings: ["Calibration warning"], metrics: [
        { id: "steps", label: "Steps", unit: "steps", current: 125, target: 100, progress: 125, available: true },
        { id: "time", label: "Walking time", unit: "seconds", current: 1799.9999, target: 1800, progress: 99.9999, available: true },
        { id: "calories", label: "Calories", unit: "kcal", current: 250, target: 200, progress: 100, available: false, reason: "Set weight <unsafe>." },
      ] },
      { id: "weekly", label: "Weekly", from_date: "2025-03-03", through_date: "2025-03-09", from: "2025-03-03T00:00:00Z", to: "2025-03-10T00:00:00Z", warnings: ["Calibration warning"], metrics: [
        { id: "steps", label: "Steps", unit: "steps", current: 0, target: 1000, progress: -5, available: true },
      ] },
      { id: "monthly", label: "Monthly", from_date: "2025-03-01", through_date: "2025-03-31", from: "2025-03-01T00:00:00Z", to: "2025-04-01T00:00:00Z", warnings: [], metrics: [] },
      { id: "yearly", label: "Yearly", from_date: "2025-01-01", through_date: "2025-12-31", from: "2025-01-01T00:00:00Z", to: "2026-01-01T00:00:00Z", warnings: [], metrics: [] },
    ],
  };
}

test("initial loading, accessible uncapped bars, unavailable metrics, placeholders and safe text", async () => {
  const h = harness();
  const el = h.elements;
  assert.equal(el.refresh.disabled, true);
  assert.equal(el["goals-periods"].attributes["aria-busy"], "true");
  assert.match(el["goals-status"].textContent, /Loading/);
  assert.equal(h.pending[0].url, "/api/goals");
  assert.equal(h.pending[0].options.cache, "no-store");
  await h.context.loadGoals(); // Ignore duplicate refreshes while loading.
  assert.equal(h.pending.length, 1);
  h.pending.shift().resolve({ ok: true, json: async () => report() });
  await settle();
  assert.equal(el.refresh.disabled, false);
  assert.equal(el["goals-periods"].children.length, 4);
  assert.equal(el["goals-periods"].attributes["aria-busy"], "false");
  const bars = descendants(el["goals-periods"], "progress");
  assert.equal(bars.length, 3); // Unavailable calories have no misleading bar.
  assert.equal(bars[0].value, 125);
  assert.equal(bars[0].max, 125);
  assert.equal(bars[0].className, "over");
  assert.equal(bars[0].style["--goal-target"], "80%");
  assert.equal(bars[0].attributes["aria-label"], "Daily Steps");
  assert.match(bars[0].attributes["aria-valuetext"], /125 steps \/ 100 steps, 125%/);
  assert.equal(bars[2].value, 0);
  assert.match(bars[1].attributes["aria-valuetext"], /29m 59\.999s \/ 30m, 99\.9%/);
  assert.match(el["goals-periods"].textContent, /UnavailableTarget: 200 kcal\. Set weight <unsafe>\./);
  assert.match(el["goals-periods"].textContent, /No monthly targets configured/);
  assert.equal(el.notices.children.length, 1); // Deduplicate period warnings.
  assert.match(el["goals-periods"].textContent, /example on Configuration/);
  assert.equal(h.device.textContent, report().device);
});

test("a dismissed step-length notice is stored and stays hidden after refresh", async () => {
  const storage = {};
  const h = harness(storage);
  const withEstimate = () => {
    const r = report();
    r.periods[0].warnings.push("Steps use an approximate step length of 41.4% of height.");
    return r;
  };
  h.pending.shift().resolve({ ok: true, json: async () => withEstimate() });
  await settle();
  assert.equal(h.elements.notices.children.length, 2);
  const [close] = descendants(h.elements.notices, "button");
  close.onclick();
  assert.equal(h.elements.notices.children.length, 1);
  assert.equal(storage["tread.dismissed-notices"], '["step-length-estimate"]');
  h.context.loadGoals();
  h.pending.shift().resolve({ ok: true, json: async () => withEstimate() });
  await settle();
  assert.equal(h.elements.notices.children.length, 1);
  assert.doesNotMatch(h.elements.notices.textContent, /approximate step length/);
});

test("refresh errors clear old bars and allow recovery with reloaded metadata", async () => {
  const h = harness();
  h.pending.shift().resolve({ ok: true, json: async () => report() });
  await settle();
  assert.ok(descendants(h.elements["goals-periods"], "progress").length);
  const refresh = h.context.loadGoals();
  assert.equal(h.elements.refresh.disabled, true);
  assert.equal(h.elements["goals-periods"].children.length, 0);
  h.pending.shift().resolve({ ok: false, text: async () => "goals.yaml: invalid <YAML>" });
  await refresh;
  assert.equal(descendants(h.elements["goals-periods"], "progress").length, 0);
  assert.match(h.elements.notices.textContent, /invalid <YAML>/);
  assert.equal(h.elements.refresh.disabled, false);
  const recovery = h.context.loadGoals();
  const next = report();
  next.timezone = "Asia/Tokyo";
  next.path = "Built-in sample targets (no file)";
  next.device = "Updated device";
  next.demo = true;
  next.periods[0].metrics[0].target = 200;
  next.periods[0].metrics[0].progress = 62.5;
  h.pending.shift().resolve({ ok: true, json: async () => next });
  await recovery;
  assert.match(h.elements["goals-calendar"].textContent, /Asia\/Tokyo/);
  assert.match(h.elements["goals-periods"].textContent, /125 steps \/ 200 steps/);
  assert.match(h.elements.notices.textContent, /Demo only: illustrative in-memory/);
  assert.equal(h.demo.hidden, false);
  assert.equal(h.device.textContent, "Updated device");
});

test("missing file shows four empty periods, not zero bars", async () => {
  const h = harness();
  const empty = report();
  empty.periods.forEach((period) => { period.metrics = []; });
  h.pending.shift().resolve({ ok: true, json: async () => empty });
  await settle();
  assert.equal(h.elements["goals-periods"].children.length, 4);
  assert.equal(descendants(h.elements["goals-periods"], "progress").length, 0);
  assert.match(h.elements["goals-status"].textContent, /missing or empty goals.yaml/);
  assert.match(h.elements["goals-status"].textContent, /See Configuration for the file path and example/);
});

test("calendar ranges use inclusive local dates across a DST boundary", async () => {
  const h = harness();
  const next = report();
  next.timezone = "America/Los_Angeles";
  const week = next.periods[1];
  week.from = "2025-03-03T00:00:00-08:00";
  week.to = "2025-03-10T00:00:00-07:00";
  h.pending.shift().resolve({ ok: true, json: async () => next });
  await settle();
  const range = h.elements["goals-periods"].children[1].children[1];
  assert.equal(range.textContent,
    `${h.context.goalDate("2025-03-03")} – ${h.context.goalDate("2025-03-09")}`);
});

test("duration does not round to completion; civil date formatting is timezone-independent", () => {
  const h = harness();
  assert.equal(h.context.goalDuration(3599.99999), "59m 59.999s");
  assert.equal(h.context.goalDuration(3661), "1h 1m 1s");
  assert.equal(h.context.goalDuration(0), "0s");
  assert.equal(h.context.goalDuration(0.5), "0.5s");
  assert.equal(h.context.goalDuration(0.0001), "<0.001s");
  const expected = new Intl.DateTimeFormat(undefined, {
    month: "short", day: "numeric", year: "numeric", timeZone: "UTC",
  }).format(new Date("2025-03-10T00:00:00Z"));
  assert.equal(h.context.goalDate("2025-03-10"), expected);
});

test("all period labels use server civil dates with differing browser and server timezones", async () => {
  const oldTZ = process.env.TZ;
  process.env.TZ = "America/Los_Angeles";
  try {
    assert.equal(new Intl.DateTimeFormat().resolvedOptions().timeZone, "America/Los_Angeles");
    for (const [timezone, from, to, date] of [
      ["Local", "2025-03-06T00:00:00+09:00", "2025-03-07T00:00:00+09:00", "2025-03-06"],
      ["America/Santiago", "2024-09-08T01:00:00-03:00", "2024-09-09T00:00:00-03:00", "2024-09-08"],
      ["Africa/Cairo", "2024-04-26T01:00:00+03:00", "2024-04-27T00:00:00+03:00", "2024-04-26"],
    ]) {
      const h = harness();
      const next = report();
      next.timezone = timezone;
      // Every card must use labels rather than reinterpret timestamps.
      next.periods.forEach((p) => Object.assign(p, { from, to, from_date: date, through_date: date }));
      h.pending.shift().resolve({ ok: true, json: async () => next });
      await settle();
      const expected = new Intl.DateTimeFormat(undefined, {
        month: "short", day: "numeric", year: "numeric", timeZone: "UTC",
      }).format(new Date(`${date}T00:00:00Z`));
      for (const card of h.elements["goals-periods"].children)
        assert.equal(card.children[1].textContent, `${expected} – ${expected}`);
    }
  } finally {
    if (oldTZ === undefined) delete process.env.TZ;
    else process.env.TZ = oldTZ;
  }
});
