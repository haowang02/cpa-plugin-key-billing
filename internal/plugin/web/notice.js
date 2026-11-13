const NOTICE_ICONS = { success: "circle-check", info: "info", warning: "triangle-alert", error: "circle-alert" };
const TOAST_LIMIT = 3;
const toastRegion = $("toasts");
const toastLifetimes = new WeakMap();
const toastHolds = new Set();
let toastReturnFocus = null;

function noticeIcon(kind) { return actionIcon(NOTICE_ICONS[kind], "notice-icon"); }

function notice(message, kind, action) {
  return el(
    "div",
    { class: "notice", "data-kind": kind },
    el("div", { class: "notice-text" }, noticeIcon(kind), el("span", { text: message })),
    action
  );
}

function activeToasts() { return [...toastRegion.children].filter((toast) => !toast.hasAttribute("data-leaving")); }

function notify(message, kind = "success") {
  const text = String(message);
  for (const toast of activeToasts()) {
    if (toast.dataset.kind === kind && toast.querySelector(".toast-message").textContent === text) dismissToast(toast);
  }
  const urgent = kind === "warning" || kind === "error";
  const toast = el(
    "div",
    { class: "toast", "data-kind": kind },
    noticeIcon(kind),
    el("div", { class: "toast-message", role: urgent ? "alert" : "status", text: message }),
    el(
      "button",
      { type: "button", class: "icon-action toast-close", "aria-label": m("ui.close"), onclick: () => dismissToast(toast) },
      actionIcon("x")
    )
  );
  toastRegion.append(toast);
  // A modal dialog opened since the region appeared sits above it in the top layer.
  if (toastRegion.matches(":popover-open") && document.querySelector("dialog:modal")) toastRegion.hidePopover();
  if (!toastRegion.matches(":popover-open")) toastRegion.showPopover();
  // An effectless animation is a lifetime that pauses and resumes with the toast.
  const lifetime = toast.animate(null, { duration: urgent ? 8000 : 4000 });
  if (toastHolds.size) lifetime.pause();
  lifetime.finished.then(() => dismissToast(toast), () => {});
  toastLifetimes.set(toast, lifetime);
  for (const stale of activeToasts().slice(0, -TOAST_LIMIT)) dismissToast(stale);
}

function dismissToast(toast) {
  if (!toast.isConnected || toast.hasAttribute("data-leaving")) return;
  toastLifetimes.get(toast).cancel();
  const focused = document.activeElement;
  if (toast.contains(focused)) {
    const toasts = activeToasts();
    const index = toasts.indexOf(toast);
    const next = (toasts[index + 1] || toasts[index - 1])?.querySelector(".toast-close");
    const target = focused.matches(":focus-visible") && (next || (toastReturnFocus?.isConnected && toastReturnFocus));
    if (target) target.focus({ preventScroll: true }); else focused.blur();
  }
  toast.setAttribute("data-leaving", "");
  toast.inert = true;
  Promise.allSettled(toast.getAnimations().map((animation) => animation.finished)).then(() => {
    toast.remove();
    if (!toastRegion.children.length) closeToastRegion();
  });
}

function clearToasts() {
  for (const toast of toastRegion.children) toastLifetimes.get(toast).cancel();
  toastRegion.replaceChildren();
  closeToastRegion();
}

function closeToastRegion() {
  if (toastRegion.matches(":popover-open")) toastRegion.hidePopover();
  // Removed toasts never report pointerleave or focusout.
  holdToasts("pointer", false);
  holdToasts("focus", false);
}

function holdToasts(reason, held) {
  const wasHeld = toastHolds.size > 0;
  if (held) toastHolds.add(reason); else toastHolds.delete(reason);
  if (wasHeld === toastHolds.size > 0) return;
  for (const toast of activeToasts()) {
    const lifetime = toastLifetimes.get(toast);
    if (wasHeld) lifetime.play(); else lifetime.pause();
  }
}

toastRegion.addEventListener("pointerenter", (event) => { if (event.pointerType !== "touch") holdToasts("pointer", true); });
toastRegion.addEventListener("pointerleave", () => holdToasts("pointer", false));
toastRegion.addEventListener("focusin", (event) => {
  if (!toastRegion.contains(event.relatedTarget)) toastReturnFocus = event.relatedTarget;
  holdToasts("focus", true);
});
toastRegion.addEventListener("focusout", (event) => { if (!toastRegion.contains(event.relatedTarget)) holdToasts("focus", false); });
toastRegion.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  event.stopPropagation();
  dismissToast(event.target.closest(".toast"));
});
document.addEventListener("visibilitychange", () => holdToasts("hidden", document.hidden));
