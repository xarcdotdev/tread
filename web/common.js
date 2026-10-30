"use strict";
// Helpers shared by every page. Load before live.js and the page script.
const $ = (id) => document.getElementById(id);
const escapeHTML = (s) =>
  String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
// At most digits fraction digits, without trailing zeros.
const numberUpTo = (n, digits = 2) =>
  Number(n).toLocaleString(undefined, { maximumFractionDigits: digits });
// Rejects with the server's error text, so pages can show why a request failed.
async function get(path, options) {
  const r = await fetch(path, options);
  if (!r.ok)
    throw new Error((await r.text()) || `Request failed (${r.status})`);
  return r.json();
}

// Dismissed notices are shared by every page; each page renders its own notices.
const dismissKey = "tread.dismissed-notices";
const dismissible = {
  "step-length-estimate": "Steps use an approximate step length",
};
function noticeID(message) {
  return Object.keys(dismissible).find((id) =>
    message.startsWith(dismissible[id]),
  );
}
function dismissedNotices() {
  try {
    return JSON.parse(localStorage.getItem(dismissKey)) || [];
  } catch {
    return [];
  }
}
function dismissNotice(id) {
  try {
    localStorage.setItem(
      dismissKey,
      JSON.stringify([...new Set([...dismissedNotices(), id])]),
    );
  } catch {
    // Without storage the notice still closes for this page view.
  }
}
