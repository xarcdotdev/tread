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
let configuration,
  report,
  period = new URLSearchParams(location.search).get("period") || "1m",
  metric = "hours",
  request;
const colors = [
  "#c1cda5",
  "#a6bb89",
  "#7d9c6a",
  "#547a55",
  "#365b45",
  "#a8bdaf",
  "#d2c8a1",
  "#aa9976",
];
async function get(path) {
  const r = await fetch(path);
  if (!r.ok)
    throw new Error((await r.text()) || `Request failed (${r.status})`);
  return r.json();
}
function notices(messages, error = false) {
  $("notices").replaceChildren();
  for (const message of messages) {
    const p = document.createElement("div");
    p.className = `notice${error ? " error" : ""}`;
    p.textContent = message;
    $("notices").append(p);
  }
}
function dateText(d) {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone:
      configuration.timezone === "Local" ? undefined : configuration.timezone,
  }).format(new Date(d));
}
function fields(id, values) {
  $(id).innerHTML = values
    .map(
      ([label, value]) =>
        `<div><span class="field-label">${escapeHTML(label)}</span><span class="field-value">${escapeHTML(value)}</span></div>`,
    )
    .join("");
}
function settings(c, demo) {
  const p = c.personal,
    t = c.treadmill;
  fields("personal", [
    ["Weight", p.weight_kg ? `${p.weight_kg} kg` : "Not set"],
    ["Height", p.height_cm ? `${p.height_cm} cm` : "Not set"],
    ["Age", p.age ? `${p.age} years` : "Not set"],
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
      p.age && p.height_cm && ["male", "female"].includes(p.gender)
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
  $("calibration-state").textContent = demo
    ? "Example values · demo only"
    : t.calibrated
      ? "Calibration confirmed"
      : "Calibration needed";
  const max = Math.max(1, ...Object.values(t.speed_kmh));
  $("calibration").innerHTML = Array.from({ length: 8 }, (_, i) => {
    const v = t.speed_kmh[i + 1];
    return `<tr><td>Level ${i + 1}</td><td>${v ? number(v, 2) + " km/h" : "Not measured"}</td><td>${v ? number(v / 3.6, 3) + " m/s" : "—"}</td><td><div class="calibration-bar"><i style="width:${v ? (v / max) * 100 : 0}%"></i></div></td></tr>`;
  }).join("");
  if (demo)
    notices([
      "Demo profile and speed values are examples. Your configuration files and database are untouched.",
    ]);
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
    max = Math.max(...entries.map((e) => e[1]), 1),
    width = viewWidth - 63,
    height = viewHeight - 70,
    base = viewHeight - 38;
  let svg = "";
  for (let i = 0; i <= 3; i++) {
    const y = base - (i * height) / 3;
    svg += `<line x1="43" y1="${y}" x2="${viewWidth - 15}" y2="${y}"/><text x="30" y="${y + 4}" text-anchor="end">${new Intl.NumberFormat(undefined, { notation: metric === "steps" ? "compact" : "standard", maximumFractionDigits: metric === "hours" ? 1 : 0 }).format((max * i) / 3)}</text>`;
  }
  const step = width / Math.max(entries.length, 1),
    barWidth = Math.min(32, step * 0.67);
  if (available)
    entries.forEach(([key, value], i) => {
      const h = (value / max) * height,
        x = 47 + i * step + (step - barWidth) / 2;
      svg += `<rect class="activity" x="${x}" y="${base - h}" width="${barWidth}" height="${h}" rx="2"><title>${key}: ${number(value, metric === "steps" ? 0 : 2)} ${unit}</title></rect>`;
      if (
        i %
          Math.max(1, Math.ceil(entries.length / (viewWidth < 450 ? 4 : 6))) ===
          0 ||
        entries.length === 1
      )
        svg += `<text x="${x + barWidth / 2}" y="${viewHeight - 11}" text-anchor="middle">${monthly ? key : key.slice(5)}</text>`;
    });
  if (!available || !report.totals.hours)
    svg += `<text x="${viewWidth / 2}" y="${base - height / 2}" text-anchor="middle">${!available ? "Complete your configuration to calculate this metric" : "No walking recorded in this period"}</text>`;
  $("activity-chart").innerHTML = svg;
  $("activity-chart").setAttribute(
    "aria-label",
    `${monthly ? "Monthly" : "Daily"} ${unit}, ${entries.length} periods. Total ${available ? number(report.totals[metric], 1) : "unavailable"}.`,
  );
  $("chart-subtitle").textContent = monthly
    ? "Your monthly walking rhythm"
    : "Your daily walking rhythm";
  document
    .querySelectorAll("[data-metric]")
    .forEach((b) =>
      b.setAttribute("aria-pressed", String(b.dataset.metric === metric)),
    );
}
function drawPace() {
  const total = report.totals.hours,
    radius = 66,
    circumference = 2 * Math.PI * radius;
  let offset = 0;
  let svg =
    '<circle cx="90" cy="90" r="66" fill="none" stroke="#f0f3eb" stroke-width="19"/>';
  report.pace.forEach((p, i) => {
    const length = total ? (p.hours / total) * circumference : 0;
    svg += `<circle cx="90" cy="90" r="66" fill="none" stroke="${colors[i % colors.length]}" stroke-width="19" stroke-dasharray="${Math.max(0, length - 2)} ${circumference}" stroke-dashoffset="${-offset}"><title>Level ${p.level}: ${number(p.hours, 1)} hours</title></circle>`;
    offset += length;
  });
  $("pace-chart").innerHTML = svg;
  $("pace-hours").textContent = number(total, 1);
  $("pace-legend").innerHTML =
    report.pace
      .map(
        (p, i) =>
          `<div class="legend-row"><i style="background:${colors[i % colors.length]}"></i><span>Level ${p.level} <span class="subtle">· ${p.kmh ? number(p.kmh, 2) + " km/h" : "uncalibrated"}</span></span><strong>${number(total ? (p.hours / total) * 100 : 0)}%</strong></div>`,
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
  const tz =
    configuration.timezone === "Local" ? undefined : configuration.timezone;
  $("sessions").innerHTML =
    report.sessions
      .slice(0, 8)
      .map((s) => {
        const hours = s.walking_ns / 3.6e12;
        return `<tr><td>${escapeHTML(new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZone: tz }).format(new Date(s.start)))}</td><td>${number(hours * 60)} min</td><td>${report.distance_available ? number(s.distance_km, 2) + " km" : "—"}</td><td>${report.distance_available ? number(hours ? s.distance_km / hours : 0, 2) + " km/h" : "—"}</td><td>${escapeHTML(s.device)}</td><td><span class="pill">Walking</span></td></tr>`;
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
async function init() {
  try {
    const { config: c, demo } = await get("/api/config");
    configuration = c;
    document
      .querySelectorAll("[data-device]")
      .forEach((el) => (el.textContent = c.treadmill.name));
    document
      .querySelectorAll("[data-demo]")
      .forEach((el) => (el.hidden = !demo));
    if ($("personal")) {
      settings(c, demo);
      return;
    }
    if (!["all", "3y", "1y", "3m", "1m", "1w", "1d"].includes(period))
      period = "1m";
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
    new ResizeObserver(() => drawChart()).observe($("activity-chart"));
    await load();
  } catch (e) {
    notices([`Could not load configuration: ${e.message}`], true);
    document.body.classList.remove("loading");
  }
}
init();
