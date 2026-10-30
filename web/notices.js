"use strict";
// Dismissed notices are shared by every page; each page renders its own notices.
// Load before the page script.
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
