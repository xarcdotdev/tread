"use strict";
// Live link status and readings from the local agent, polled while visible.
// Readings are what the treadmill reports, never commanded or estimated values.
(() => {
  const linkText = {
    off: "Agent not running",
    connected: "Connected",
    connecting: "Connecting…",
    disconnected: "Disconnected",
    fault: "Connection fault",
    disabled: "Bluetooth disabled",
  };
  const labels = {
    speed: "Speed",
    distance: "Distance",
    elapsed: "Time",
    calories: "Calories",
    incline: "Incline",
    pulse: "Pulse",
  };
  function reading(m) {
    if (m.unit === "s") {
      const t = Math.round(m.value);
      return `${Math.floor(t / 60)}:${String(t % 60).padStart(2, "0")}`;
    }
    const n = numberUpTo(m.value);
    return m.unit === "unknown" ? n : `${n}<small>${escapeHTML(m.unit)}</small>`;
  }
  async function poll() {
    let live;
    try {
      const r = await fetch("/api/live", { cache: "no-store" });
      live = r.ok
        ? await r.json()
        : { available: false, reason: `HTTP ${r.status}` };
    } catch (e) {
      live = { available: false, reason: e.message };
    }
    const s = live.available ? live.snapshot : null;
    const link = s ? s.connection : "off";
    document.querySelectorAll(".device-card").forEach((card) => {
      card.dataset.link = link;
      card.title = s ? "" : live.reason || "";
    });
    document
      .querySelectorAll("[data-link-label]")
      .forEach((el) => (el.textContent = linkText[link] || link));
    const panel = $("live");
    if (!panel) return;
    const active = s && s.connection === "connected" && s.telemetry === "active";
    panel.hidden = !active;
    if (!active) return;
    const stats = [];
    const state = s.state.value;
    if (state && state !== "unknown")
      stats.push([state[0].toUpperCase() + state.slice(1), "State", s.state.stale]);
    for (const [field, label] of Object.entries(labels)) {
      const m = s.measurements?.[field];
      if (m && m.value !== null && m.value !== undefined)
        stats.push([reading(m), label, m.stale]);
    }
    $("live-stats").innerHTML = stats
      .map(
        ([value, label, stale]) =>
          `<div class="stat${stale ? " stale" : ""}"><strong>${value}</strong><span>${label}${stale ? " · stale" : ""}</span></div>`,
      )
      .join("");
  }
  poll();
  setInterval(() => document.hidden || poll(), 2000);
  document.addEventListener("visibilitychange", () => document.hidden || poll());
})();
