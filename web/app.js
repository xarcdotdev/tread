"use strict";
const $ = (id) => document.getElementById(id);
const number = (n, digits = 0) =>
  Number(n).toLocaleString(undefined, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
const escapeHTML = (s) =>
  String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const icon = (name) =>
  `<svg class="icon" aria-hidden="true"><use href="/icons.svg#${name}"/></svg>`;
// Speed levels are ordinal, so each level keeps its own step of one ramp.
const levelColor = (level) =>
  `var(--level-${Math.min(8, Math.max(1, level))})`;
let configuration,
  report,
  period = new URLSearchParams(location.search).get("period") || "1d",
  metric = "hours",
  bars = [],
  request;
async function get(path) {
  const r = await fetch(path);
  if (!r.ok)
    throw new Error((await r.text()) || `Request failed (${r.status})`);
  return r.json();
}
// Dismissal state comes from notices.js.
function notices(messages, error = false) {
  const dismissed = dismissedNotices();
  $("notices").innerHTML = messages
    .filter((m) => !dismissed.includes(noticeID(m)))
    .map((m) => {
      const id = noticeID(m);
      const close = id
        ? `<button type="button" class="notice-close" data-dismiss="${id}" aria-label="Dismiss notice">×</button>`
        : "";
      return `<div class="notice${error ? " error" : ""}">${icon(error ? "circle-alert" : "triangle-alert")}<span>${escapeHTML(m)}</span>${close}</div>`;
    })
    .join("");
  $("notices").onclick = (e) => {
    const close = e.target.closest?.("[data-dismiss]");
    if (!close) return;
    dismissNotice(close.dataset.dismiss);
    close.closest(".notice").remove();
  };
}
function timeZone() {
  return configuration.timezone === "Local" ? undefined : configuration.timezone;
}
function dateText(d) {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: timeZone(),
  }).format(new Date(d));
}
// Hourly keys are instants; daily keys are civil dates, formatted as-is.
function bucketText(key, monthly, long) {
  if (report?.period === "1d") {
    const options = {
      hour: "2-digit", minute: "2-digit", timeZone: timeZone(),
      ...(long ? { month: "short", day: "numeric", timeZoneName: "shortOffset" } : {}),
    };
    const format = new Intl.DateTimeFormat(undefined, options);
    const start = new Date(key);
    return long
      ? `${format.format(start)} – ${format.format(new Date(start.getTime() + 3600000))}`
      : format.format(start);
  }
  const [y, m, d] = key.split("-").map(Number);
  return new Intl.DateTimeFormat(
    undefined,
    monthly
      ? { month: "short", year: long ? "numeric" : "2-digit" }
      : long
        ? { weekday: "short", month: "short", day: "numeric", year: "numeric" }
        : { month: "short", day: "numeric" },
  ).format(new Date(y, m - 1, d || 1));
}
function fields(id, values) {
  const unset = ["Not set", "Not configured"];
  $(id).innerHTML = values
    .map(
      ([label, value]) =>
        `<div><span class="field-label">${escapeHTML(label)}</span><span class="field-value${unset.includes(value) ? " unset" : ""}">${escapeHTML(value)}</span></div>`,
    )
    .join("");
}
function settings(c, demo, age = null) {
  const p = c.personal,
    t = c.treadmill;
  fields("personal", [
    ["Weight", p.weight_kg ? `${p.weight_kg} kg` : "Not set"],
    ["Height", p.height_cm ? `${p.height_cm} cm` : "Not set"],
    ["Birthdate", p.birthdate || "Not set"],
    ["Age", age !== null ? `${age} years` : "Not set"],
    ["Gender", p.gender || "Not set"],
    [
      "Step length",
      p.step_length_m
        ? `${p.step_length_m} m`
        : p.height_cm
          ? `${number((p.height_cm / 100) * 0.414, 3)} m · estimated from height`
          : "Not set",
    ],
    [
      "Calorie model",
      age > 0 && p.weight_kg > 0 && p.height_cm > 0 &&
      ["male", "female"].includes(p.gender)
        ? "MET × personalized resting energy"
        : "MET × weight × hours",
    ],
  ]);
  fields("device", [
    ["Name", t.name],
    ["Driver", t.driver.toUpperCase()],
    ["Bluetooth address", t.mac || "Not configured"],
    ["Device identifier", c.device],
    ["Initial speed level", t.initial_level],
    ["Connection", "Linux · Bluetooth LE / BlueZ"],
  ]);
  fields("application", [
    ["Configuration file", c.path],
    ["Server URL", c.server_url || "Offline queue only"],
    ["SQLite database", demo ? "Isolated in-memory demo" : c.db_path],
    ["Listen address", c.listen],
    ["Statistics timezone", c.timezone],
    [
      "Config source",
      demo ? "Built-in mock profile" : "TOML file · reloaded on request",
    ],
  ]);
  const [state, stateIcon, stateText] = demo
    ? ["", "info", "Example values · demo only"]
    : t.calibrated
      ? ["good", "circle-check", "Calibration confirmed"]
      : ["warn", "triangle-alert", "Calibration needed"];
  $("calibration-state").className = `chip ${state}`;
  $("calibration-state").innerHTML = `${icon(stateIcon)}${stateText}`;
  const max = Math.max(1, ...Object.values(t.speed_kmh));
  $("calibration").innerHTML = Array.from({ length: 8 }, (_, i) => {
    const v = t.speed_kmh[i + 1];
    return `<tr><td><span class="level-tag"><i style="background:${levelColor(i + 1)}"></i>Level ${i + 1}</span></td><td>${v ? number(v, 2) + " km/h" : "Not measured"}</td><td>${v ? number(v / 3.6, 3) + " m/s" : "—"}</td><td><div class="calibration-bar"><i style="width:${v ? (v / max) * 100 : 0}%"></i></div></td></tr>`;
  }).join("");
  if (demo)
    notices([
      "Demo profile and speed values are examples. Your configuration files and database are untouched.",
    ]);
}
// Round the axis maximum up so the three gridlines land on clean values.
function niceMax(max) {
  const raw = max / 3,
    pow = 10 ** Math.floor(Math.log10(raw));
  return [1, 2, 5, 10].find((m) => m * pow >= raw) * pow * 3;
}
function drawChart() {
  if (!report) return;
  const available =
    metric === "hours" ||
    (metric === "steps" && report.steps_available) ||
    (metric === "distance_km" && report.distance_available);
  const unit = {
    hours: "hours walked",
    distance_km: "kilometres walked",
    steps: "estimated steps",
  }[metric];
  const digits = metric === "steps" ? 0 : metric === "hours" ? 1 : 2;
  $("chart-total").textContent = available
    ? number(report.totals[metric], metric === "steps" ? 0 : 1)
    : "—";
  $("chart-unit").textContent = unit;
  const monthly = report.days.length > 93,
    groups = new Map();
  for (const day of report.days) {
    const key = monthly ? day.date.slice(0, 7) : day.date;
    groups.set(key, (groups.get(key) || 0) + day[metric]);
  }
  const chart = $("activity-chart"),
    viewWidth = Math.max(280, chart.clientWidth),
    viewHeight = chart.clientHeight;
  chart.setAttribute("viewBox", `0 0 ${viewWidth} ${viewHeight}`);
  const entries = [...groups],
    max = niceMax(Math.max(...entries.map((e) => e[1]), 0) || 1),
    left = 44,
    top = 10,
    width = viewWidth - left - 4,
    height = viewHeight - top - 30,
    base = top + height,
    ticks = new Intl.NumberFormat(undefined, {
      notation: metric === "steps" ? "compact" : "standard",
      maximumFractionDigits: max < 3 ? 1 : 0,
    });
  let grid = "",
    marks = "",
    labels = "";
  for (let i = 0; i <= 3; i++) {
    const y = base - (i * height) / 3;
    grid += `<line${i ? "" : ' class="baseline"'} x1="${left}" y1="${y}" x2="${viewWidth}" y2="${y}"/><text x="${left - 12}" y="${y + 4}" text-anchor="end">${ticks.format((max * i) / 3)}</text>`;
  }
  const step = width / Math.max(entries.length, 1),
    barWidth = Math.max(1, Math.min(24, step * 0.62, step - 2)),
    every = Math.max(1, Math.ceil(entries.length / (viewWidth < 450 ? 4 : 7)));
  bars = [];
  if (available)
    entries.forEach(([key, value], i) => {
      const h = (value / max) * height,
        x = left + i * step + (step - barWidth) / 2,
        r = Math.min(4, barWidth / 2, h);
      bars.push({ key, value, x: x + barWidth / 2, y: base - h });
      marks += `<rect class="band" data-i="${i}" x="${left + i * step}" y="${top}" width="${step}" height="${height}"/>`;
      if (h > 0)
        marks += `<path class="bar" data-bar="${i}" pointer-events="none" d="M${x} ${base}V${base - h + r}a${r} ${r} 0 0 1 ${r} ${-r}H${x + barWidth - r}a${r} ${r} 0 0 1 ${r} ${r}V${base}Z"/>`;
      if (i % every === 0)
        labels += `<text x="${x + barWidth / 2}" y="${viewHeight - 8}" text-anchor="middle">${bucketText(key, monthly)}</text>`;
    });
  if (!available || !report.totals.hours)
    labels += `<text class="empty-text" x="${left + width / 2}" y="${top + height / 2}" text-anchor="middle">${!available ? "Complete your configuration to calculate this metric" : "No walking recorded in this period"}</text>`;
  chart.innerHTML = grid + marks + labels;
  chart.dataset.monthly = monthly ? "1" : "";
  chart.dataset.digits = digits;
  chart.setAttribute(
    "aria-label",
    `${report.period === "1d" ? "Hourly" : monthly ? "Monthly" : "Daily"} ${unit}, ${entries.length} periods. Total ${available ? number(report.totals[metric], 1) : "unavailable"}.`,
  );
  $("chart-tooltip").hidden = true;
  document
    .querySelectorAll("[data-metric]")
    .forEach((b) =>
      b.setAttribute("aria-pressed", String(b.dataset.metric === metric)),
    );
}
function hoverChart(e) {
  const chart = $("activity-chart"),
    tip = $("chart-tooltip"),
    i = e.target.dataset?.i;
  chart
    .querySelectorAll(".hover")
    .forEach((el) => el.classList.remove("hover"));
  if (i === undefined) {
    tip.hidden = true;
    return;
  }
  const bar = bars[i];
  e.target.classList.add("hover");
  chart.querySelector(`[data-bar="${i}"]`)?.classList.add("hover");
  tip.innerHTML = `<span>${escapeHTML(bucketText(bar.key, chart.dataset.monthly, true))}</span><strong>${number(bar.value, Number(chart.dataset.digits))}</strong> ${escapeHTML($("chart-unit").textContent)}`;
  tip.hidden = false;
  const half = tip.offsetWidth / 2;
  tip.style.left = `${Math.min(Math.max(bar.x, half), chart.clientWidth - half)}px`;
  tip.style.top = `${bar.y}px`;
}
function drawPace() {
  const total = report.totals.hours,
    radius = 70,
    circumference = 2 * Math.PI * radius,
    used = report.pace.filter((p) => p.hours > 0),
    gap = used.length > 1 ? 2 : 0;
  let offset = 0;
  let svg = total
    ? ""
    : `<circle cx="90" cy="90" r="${radius}" fill="none" stroke="var(--line-2)" stroke-width="18"/>`;
  for (const p of used) {
    const length = total ? (p.hours / total) * circumference : 0;
    svg += `<circle cx="90" cy="90" r="${radius}" fill="none" style="stroke:${levelColor(p.level)}" stroke-width="18" stroke-dasharray="${Math.max(0, length - gap)} ${circumference}" stroke-dashoffset="${-offset}"><title>Level ${p.level}: ${number(p.hours, 1)} hours</title></circle>`;
    offset += length;
  }
  $("pace-chart").innerHTML = svg;
  $("pace-hours").textContent = number(total, 1);
  $("pace-legend").innerHTML =
    report.pace
      .map(
        (p) =>
          `<div class="legend-row"><i style="background:${levelColor(p.level)}"></i><span>Level ${p.level} <span class="subtle">· ${p.kmh ? number(p.kmh, 2) + " km/h" : "uncalibrated"}</span></span><span class="subtle">${number(p.hours, 1)} h</span><strong>${number(total ? (p.hours / total) * 100 : 0)}%</strong></div>`,
      )
      .join("") ||
    '<p class="empty">Your pace mix will appear after your first walk.</p>';
}
function render() {
  const t = report.totals;
  $("hours").textContent = number(t.hours, 1);
  $("steps").textContent = report.steps_available ? number(t.steps) : "—";
  $("speed").textContent = report.distance_available
    ? number(t.average_speed_kmh, 2)
    : "—";
  $("calories").textContent = report.calories_available
    ? number(t.calories)
    : "—";
  $("distance").textContent = report.distance_available
    ? number(t.distance_km, 1)
    : "—";
  $("session-count").textContent =
    `${number(report.sessions.length)} sessions · pauses excluded`;
  $("date-range").textContent =
    `${dateText(report.from)} – ${dateText(report.to)}`;
  notices(report.warnings);
  const day = new Intl.DateTimeFormat(undefined, {
      weekday: "short",
      month: "short",
      day: "numeric",
      timeZone: timeZone(),
    }),
    time = new Intl.DateTimeFormat(undefined, {
      hour: "2-digit",
      minute: "2-digit",
      timeZone: timeZone(),
    });
  $("sessions").innerHTML =
    report.sessions
      .slice(0, 8)
      .map((s) => {
        const hours = s.walking_ns / 3.6e12,
          start = new Date(s.start);
        return `<tr><td><div class="when"><span class="when-icon">${icon("footprints")}</span><div><strong>${escapeHTML(day.format(start))}</strong><span>${escapeHTML(time.format(start))}</span></div></div></td><td class="strong">${number(hours * 60)} min</td><td>${report.distance_available ? number(s.distance_km, 2) + " km" : "—"}</td><td>${report.distance_available ? number(hours ? s.distance_km / hours : 0, 2) + " km/h" : "—"}</td><td>${escapeHTML(s.device)}</td><td><span class="pill">Walking</span></td></tr>`;
      })
      .join("") ||
    '<tr><td colspan="6" class="empty">No sessions yet. Use tread start, speed and stop to record your first walk.</td></tr>';
  $("sessions-note").textContent =
    `Showing ${Math.min(8, report.sessions.length)} of ${number(report.sessions.length)} sessions in this period`;
  drawChart();
  drawPace();
  document.body.classList.remove("loading");
}
async function load() {
  const token = {};
  request = token;
  document.body.classList.add("loading");
  $("refresh").disabled = true;
  document
    .querySelectorAll("[data-period]")
    .forEach((b) =>
      b.setAttribute("aria-pressed", String(b.dataset.period === period)),
    );
  try {
    const next = await get(`/api/stats?period=${encodeURIComponent(period)}`);
    if (request !== token) return;
    report = next;
    render();
    history.replaceState(null, "", `?period=${period}`);
  } catch (e) {
    if (request !== token) return;
    notices([`Could not load statistics: ${e.message}`], true);
  } finally {
    if (request === token) {
      $("refresh").disabled = false;
      document.body.classList.remove("loading");
    }
  }
}
async function loadGoalsPath() {
  try {
    const goals = await get("/api/goals");
    $("goals-path").textContent = goals.path;
  } catch (e) {
    $("goals-path").textContent = `Unavailable: ${e.message}`;
  }
}
async function init() {
  try {
    const { config: c, demo, age } = await get("/api/config");
    configuration = c;
    document
      .querySelectorAll("[data-device]")
      .forEach((el) => (el.textContent = c.treadmill.name));
    document
      .querySelectorAll("[data-demo]")
      .forEach((el) => (el.hidden = !demo));
    if ($("personal")) {
      settings(c, demo, age);
      await loadGoalsPath();
      return;
    }
    if (!["all", "3y", "1y", "3m", "1m", "1w", "1d"].includes(period))
      period = "1d";
    $("today").textContent = dateText(new Date());
    document.querySelectorAll("[data-period]").forEach(
      (b) =>
        (b.onclick = () => {
          period = b.dataset.period;
          load();
        }),
    );
    document.querySelectorAll("[data-metric]").forEach(
      (b) =>
        (b.onclick = () => {
          metric = b.dataset.metric;
          drawChart();
        }),
    );
    $("refresh").onclick = load;
    const chart = $("activity-chart");
    chart.onpointermove = chart.onpointerdown = hoverChart;
    chart.onpointerleave = () => hoverChart({ target: chart });
    new ResizeObserver(() => drawChart()).observe(chart);
    await load();
  } catch (e) {
    notices([`Could not load configuration: ${e.message}`], true);
    document.body.classList.remove("loading");
  }
}
init();
