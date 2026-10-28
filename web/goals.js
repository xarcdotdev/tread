"use strict";

// This page is independent of the overview's rolling statistics controls.
const goalElement = (tag, text, className) => {
  const element = document.createElement(tag);
  if (text !== undefined) element.textContent = text;
  if (className) element.className = className;
  return element;
};
const goalNumber = (n, digits = 2) =>
  Number(n).toLocaleString(undefined, { maximumFractionDigits: digits });

function goalDuration(seconds) {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  // Truncate rather than round a nearly completed minute/hour up to the target.
  const truncated = Math.floor(remainder * 1000) / 1000;
  const parts = [];
  if (hours) parts.push(`${goalNumber(hours, 0)}h`);
  if (minutes) parts.push(`${minutes}m`);
  if (remainder || !parts.length)
    parts.push(`${remainder > 0 && truncated === 0 ? "<0.001" : goalNumber(truncated, 3)}s`);
  return parts.join(" ");
}
function goalValue(value, unit) {
  return unit === "seconds"
    ? goalDuration(value)
    : `${goalNumber(value, unit === "steps" ? 0 : 2)} ${unit}`;
}
function goalPercent(progress) {
  return Math.min(100, Math.max(0, progress));
}
function goalDate(value) {
  // Server-provided civil date, including when its timezone is Local.
  // UTC is only a formatting carrier, never the browser's timezone.
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  }).format(new Date(`${value}T00:00:00Z`));
}
function goalMetric(period, metric) {
  const row = goalElement("div", undefined, "goal-metric");
  const heading = goalElement("div", undefined, "goal-metric-head");
  heading.append(goalElement("h3", metric.label));
  row.append(heading);
  if (!metric.available) {
    heading.append(goalElement("span", "Unavailable", "goal-unavailable"));
    row.append(goalElement("p", `Target: ${goalValue(metric.target, metric.unit)}. ${metric.reason}`, "goal-reason"));
    return row;
  }
  const progress = goalPercent(metric.progress);
  // Never label a value below target as 100% due to display rounding.
  const percent = `${goalNumber(Math.floor(progress * 10) / 10, 1)}%`;
  heading.append(goalElement("span", percent, "goal-percent"));
  const values = `${goalValue(metric.current, metric.unit)} / ${goalValue(metric.target, metric.unit)}`;
  row.append(goalElement("p", values, "goal-values"));
  const bar = goalElement("progress");
  bar.max = 100;
  bar.value = progress;
  bar.setAttribute("aria-label", `${period.label} ${metric.label}`);
  bar.setAttribute("aria-valuetext", `${values}, ${percent}`);
  bar.textContent = percent;
  row.append(bar);
  return row;
}
function renderGoals(report) {
  const byID = (id) => document.getElementById(id);
  byID("goals-calendar").textContent = `Calendar days, Monday-start weeks, months and years in ${report.timezone}. Progress counts walking through now. Unlike the overview, these are not rolling windows.`;
  document.querySelectorAll("[data-device]").forEach((el) => (el.textContent = report.device));
  document.querySelectorAll("[data-demo]").forEach((el) => (el.hidden = !report.demo));
  const warnings = new Set();
  if (report.demo)
    warnings.add("Demo only: illustrative in-memory targets and sample history. Your real goals.yaml, profile and database are not read or changed.");
  let configured = false;
  for (const period of report.periods) {
    const card = goalElement("article", undefined, "card panel goal-period");
    const title = goalElement("h2", period.label);
    title.id = `goal-${period.id}`;
    card.setAttribute("aria-labelledby", title.id);
    // The server supplies the first and last included calendar dates.
    card.append(title, goalElement("p", `${goalDate(period.from_date)} – ${goalDate(period.through_date)}`, "goal-range"));
    if (!period.metrics.length) {
      card.append(goalElement("p", `No ${period.id} targets configured. Add a ${period.id}: section to goals.yaml using the example on Configuration, then Refresh.`, "goal-placeholder"));
    } else {
      configured = true;
      for (const metric of period.metrics) card.append(goalMetric(period, metric));
      for (const warning of period.warnings || []) warnings.add(warning);
    }
    byID("goals-periods").append(card);
  }
  for (const warning of warnings)
    byID("notices").append(goalElement("div", warning, "notice"));
  byID("goals-status").textContent = configured
    ? ""
    : "No goals configured. A missing or empty goals.yaml is fine. See Configuration for the file path and example, then set your targets.";
}
async function loadGoals() {
  const byID = (id) => document.getElementById(id);
  const refresh = byID("refresh");
  if (refresh.dataset.loading) return;
  refresh.dataset.loading = "true";
  refresh.disabled = true;
  document.body.classList.add("loading");
  byID("goals-periods").replaceChildren();
  byID("goals-periods").setAttribute("aria-busy", "true");
  byID("notices").replaceChildren();
  byID("goals-status").textContent = "Loading goals…";
  try {
    const response = await fetch("/api/goals", { cache: "no-store" });
    if (!response.ok)
      throw new Error((await response.text()) || `Request failed (${response.status})`);
    renderGoals(await response.json());
  } catch (error) {
    // A bad edit must not leave old bars or display misleading zero completion.
    byID("goals-periods").replaceChildren();
    byID("notices").replaceChildren(goalElement("div", `Could not load goals: ${error.message}`, "notice error"));
    byID("goals-status").textContent = "Progress is unavailable. Fix the file on the server, then Refresh.";
  } finally {
    delete refresh.dataset.loading;
    refresh.disabled = false;
    byID("goals-periods").setAttribute("aria-busy", "false");
    document.body.classList.remove("loading");
  }
}
document.getElementById("refresh").onclick = loadGoals;
loadGoals();
