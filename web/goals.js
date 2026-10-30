"use strict";

// This page is independent of the overview's rolling statistics controls.
// $, get, numberUpTo and notice dismissal come from common.js.
const goalElement = (tag, text, className) => {
  const element = document.createElement(tag);
  if (text !== undefined) element.textContent = text;
  if (className) element.className = className;
  return element;
};

function goalDuration(seconds) {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  // Truncate rather than round a nearly completed minute/hour up to the target.
  const truncated = Math.floor(remainder * 1000) / 1000;
  const parts = [];
  if (hours) parts.push(`${numberUpTo(hours, 0)}h`);
  if (minutes) parts.push(`${minutes}m`);
  if (remainder || !parts.length)
    parts.push(`${remainder > 0 && truncated === 0 ? "<0.001" : numberUpTo(truncated, 3)}s`);
  return parts.join(" ");
}
function goalValue(value, unit) {
  return unit === "seconds"
    ? goalDuration(value)
    : `${numberUpTo(value, unit === "steps" ? 0 : 2)} ${unit}`;
}
function goalPercent(progress) {
  return Math.max(0, progress);
}
function goalNotice(message) {
  const id = noticeID(message);
  const notice = goalElement("div", undefined, "notice");
  notice.append(goalElement("span", message));
  if (id) {
    const close = goalElement("button", "×", "notice-close");
    close.type = "button";
    close.setAttribute("aria-label", "Dismiss notice");
    close.onclick = () => {
      dismissNotice(id);
      notice.remove();
    };
    notice.append(close);
  }
  return notice;
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
  const percent = `${numberUpTo(Math.floor(progress * 10) / 10, 1)}%`;
  heading.append(goalElement("span", percent, `goal-percent${progress > 100 ? " over" : progress >= 100 ? " met" : ""}`));
  const values = `${goalValue(metric.current, metric.unit)} / ${goalValue(metric.target, metric.unit)}`;
  row.append(goalElement("p", values, "goal-values"));
  // Past the target the bar rescales: the met target stays green, the excess turns red.
  const bar = goalElement("progress", undefined, progress > 100 ? "over" : progress >= 100 ? "met" : undefined);
  bar.max = Math.max(100, progress);
  bar.value = progress;
  if (progress > 100) bar.style.setProperty("--goal-target", `${(100 / progress) * 100}%`);
  bar.setAttribute("aria-label", `${period.label} ${metric.label}`);
  bar.setAttribute("aria-valuetext", `${values}, ${percent}`);
  bar.textContent = percent;
  row.append(bar);
  return row;
}
function renderGoals(report) {
  $("goals-calendar").textContent = `Calendar days, Monday-start weeks, months and years in ${report.timezone}. Progress counts walking through now. Unlike the overview, these are not rolling windows.`;
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
    $("goals-periods").append(card);
  }
  const dismissed = dismissedNotices();
  for (const warning of warnings)
    if (!dismissed.includes(noticeID(warning))) $("notices").append(goalNotice(warning));
  $("goals-status").textContent = configured
    ? ""
    : "No goals configured. A missing or empty goals.yaml is fine. See Configuration for the file path and example, then set your targets.";
}
async function loadGoals() {
  const refresh = $("refresh");
  if (refresh.dataset.loading) return;
  refresh.dataset.loading = "true";
  refresh.disabled = true;
  document.body.classList.add("loading");
  $("goals-periods").replaceChildren();
  $("goals-periods").setAttribute("aria-busy", "true");
  $("notices").replaceChildren();
  $("goals-status").textContent = "Loading goals…";
  try {
    renderGoals(await get("/api/goals", { cache: "no-store" }));
  } catch (error) {
    // A bad edit must not leave old bars or display misleading zero completion.
    $("goals-periods").replaceChildren();
    $("notices").replaceChildren(goalElement("div", `Could not load goals: ${error.message}`, "notice error"));
    $("goals-status").textContent = "Progress is unavailable. Fix the file on the server, then Refresh.";
  } finally {
    delete refresh.dataset.loading;
    refresh.disabled = false;
    $("goals-periods").setAttribute("aria-busy", "false");
    document.body.classList.remove("loading");
  }
}
$("refresh").onclick = loadGoals;
loadGoals();
