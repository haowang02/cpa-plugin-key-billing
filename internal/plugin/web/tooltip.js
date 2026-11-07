// One popover serves every [data-tip] anchor. A mouse has to rest on an anchor before its tooltip opens;
// while one is open, or just after it closes, the next opens at once. Keyboard focus and touch taps open it directly.
const HOVER_TOOLTIP_DELAY = 400;
const HOVER_TOOLTIP_SKIP_DELAY = 300;
let hoverTooltipAnchor = null;
let hoverTooltipPending = null;
let hoverTooltipPoint = null;
let hoverTooltipSource = "keyboard";
let hoverTooltipTimer = 0;
let hoverTooltipWarmUntil = 0;
// Popover focus changes fire focusin synchronously.
let hoverTooltipTransitioning = false;

function hoverTooltipProps(content, enabled = Boolean(content)) {
  return { tabindex: enabled ? "0" : null, tooltip: enabled ? content : null };
}

// A render may replace an anchor under a resting pointer.
function currentTooltipAnchor(anchor, point) {
  return anchor.isConnected ? anchor : point && document.elementFromPoint(point.x, point.y)?.closest("[data-tip]");
}

function closeHoverTooltip() {
  clearTimeout(hoverTooltipTimer);
  hoverTooltipTimer = 0;
  hoverTooltipPending = null;
  if (hoverTooltipAnchor) {
    const describedBy = (hoverTooltipAnchor.getAttribute("aria-describedby") || "").split(/\s+/)
      .filter((id) => id && id !== "hover-tooltip");
    if (describedBy.length) hoverTooltipAnchor.setAttribute("aria-describedby", describedBy.join(" "));
    else hoverTooltipAnchor.removeAttribute("aria-describedby");
    hoverTooltipWarmUntil = performance.now() + HOVER_TOOLTIP_SKIP_DELAY;
  }
  hoverTooltipAnchor = null;
  const tooltip = $("hover-tooltip");
  if (tooltip.matches(":popover-open")) {
    hoverTooltipTransitioning = true;
    try { tooltip.hidePopover(); } finally { hoverTooltipTransitioning = false; }
  }
}

function scheduleHoverTooltipOpen(anchor, point) {
  if (hoverTooltipAnchor || performance.now() < hoverTooltipWarmUntil) return showHoverTooltip(anchor, { point, source: "mouse" });
  clearTimeout(hoverTooltipTimer);
  hoverTooltipPending = anchor;
  hoverTooltipTimer = setTimeout(() => {
    hoverTooltipPending = null;
    const current = currentTooltipAnchor(anchor, point);
    if (current) showHoverTooltip(current, { point, source: "mouse" });
  }, HOVER_TOOLTIP_DELAY);
}

function scheduleHoverTooltipClose(next) {
  if (hoverTooltipPending && !hoverTooltipPending.contains(next)) closeHoverTooltip();
  const tooltip = $("hover-tooltip");
  if (!hoverTooltipAnchor || (next && (tooltip.contains(next) || hoverTooltipAnchor.contains(next)))) return;
  if (hoverTooltipSource === "touch") return;
  clearTimeout(hoverTooltipTimer);
  if (next?.closest?.("button, input, select, textarea, a[href]")) closeHoverTooltip();
  else hoverTooltipTimer = setTimeout(closeHoverTooltip, 150);
}

function refreshHoverTooltip() {
  if (!hoverTooltipAnchor) return;
  const anchor = currentTooltipAnchor(hoverTooltipAnchor, hoverTooltipPoint);
  if (anchor) showHoverTooltip(anchor, { point: hoverTooltipPoint, source: hoverTooltipSource }); else closeHoverTooltip();
}

function showHoverTooltip(anchor, { point = null, source = "keyboard" } = {}) {
  if (hoverTooltipTransitioning) return;
  const render = tooltipContents.get(anchor);
  const text = anchor.dataset.tip || anchor.getAttribute("aria-label");
  const masked = anchor.closest(".mask-blur") || anchor.querySelector(".mask-blur");
  const content = render ? render() : text ? [el("div", { class: "hover-tooltip-body" + (masked ? " mask-blur" : ""), text })] : null;
  closeHoverTooltip();
  if (anchor.hasAttribute("data-tip-overflow") && anchor.scrollWidth <= anchor.clientWidth + 1) return;
  if (!content?.length) return;
  const tooltip = $("hover-tooltip");
  // A modal dialog makes the rest of the document inert, so the tooltip opens inside the anchor's dialog.
  (anchor.closest("dialog") || document.body).append(tooltip);
  hoverTooltipAnchor = anchor;
  hoverTooltipPoint = point;
  hoverTooltipSource = source;
  const describedBy = anchor.getAttribute("aria-describedby");
  anchor.setAttribute("aria-describedby", [describedBy, tooltip.id].filter(Boolean).join(" "));
  tooltip.replaceChildren(...content);
  tooltip.style.maxHeight = "";
  hoverTooltipTransitioning = true;
  try { tooltip.showPopover(); } finally { hoverTooltipTransitioning = false; }
  tooltip.scrollTop = 0;
  positionHoverTooltip(anchor);
}

function positionHoverTooltip(anchor) {
  positionAnchoredPopover($("hover-tooltip"), anchor, { align: "center", side: "top", gap: 8, maxHeight: "min(320px, 55dvh)" });
}

document.addEventListener("pointerover", (event) => {
  const tooltip = $("hover-tooltip");
  if (tooltip.contains(event.target)) {
    clearTimeout(hoverTooltipTimer);
    return;
  }
  if (event.pointerType === "touch") return;
  const anchor = event.target.closest("[data-tip]");
  if (!anchor) scheduleHoverTooltipClose(event.target);
  else if (anchor === hoverTooltipAnchor) clearTimeout(hoverTooltipTimer);
  else if (anchor !== hoverTooltipPending) scheduleHoverTooltipOpen(anchor, { x: event.clientX, y: event.clientY });
});
document.addEventListener("pointerout", (event) => { if (event.pointerType !== "touch") scheduleHoverTooltipClose(event.relatedTarget); });
document.addEventListener("focusin", (event) => {
  if ($("hover-tooltip").contains(event.target)) {
    clearTimeout(hoverTooltipTimer);
    return;
  }
  const anchor = event.target.closest("[data-tip]");
  if (anchor && !hoverTooltipTransitioning) {
    // Pointer presses focus too; only keyboard focus opens a tooltip.
    queueMicrotask(() => {
      const focused = document.activeElement;
      if (anchor.contains(focused) && focused.matches(":focus-visible")) showHoverTooltip(anchor);
    });
  }
});
document.addEventListener("focusout", (event) => scheduleHoverTooltipClose(event.relatedTarget));
document.addEventListener("keydown", (event) => {
  if (event.key === "Tab") closeHoverTooltip();
  if (event.key === "Escape" && hoverTooltipAnchor) {
    event.preventDefault();
    closeHoverTooltip();
  } else if (event.key === "ArrowDown" && tooltipContents.has(event.target)) {
    event.preventDefault();
    showHoverTooltip(event.target);
    $("hover-tooltip").focus({ preventScroll: true });
  }
});
document.addEventListener("pointerdown", (event) => {
  if (!$("hover-tooltip").contains(event.target) && !hoverTooltipAnchor?.contains(event.target)) closeHoverTooltip();
});
document.addEventListener("pointerup", (event) => {
  const anchor = event.target.closest("[data-tip]");
  if (event.pointerType === "touch" && anchor) {
    if (anchor === hoverTooltipAnchor && hoverTooltipSource === "touch") closeHoverTooltip();
    else showHoverTooltip(anchor, { source: "touch" });
  }
});
window.addEventListener("resize", closeHoverTooltip);
document.addEventListener(
  "scroll",
  (event) => {
    // Content scrolling under the pointer is not a rest on the pending anchor.
    if (hoverTooltipPending) return closeHoverTooltip();
    const tooltip = $("hover-tooltip"), anchor = hoverTooltipAnchor;
    if (!anchor || tooltip.contains(event.target)) return;
    const bounds = anchor.getBoundingClientRect();
    const hovered = hoverTooltipPoint && document.elementFromPoint(hoverTooltipPoint.x, hoverTooltipPoint.y);
    if (
      bounds.bottom <= 0 || bounds.top >= innerHeight || bounds.right <= 0 || bounds.left >= innerWidth ||
      (!anchor.contains(document.activeElement) && !tooltip.contains(document.activeElement) && !anchor.contains(hovered) &&
        !tooltip.contains(hovered))
    )
      closeHoverTooltip();
    else positionHoverTooltip(anchor);
  },
  true
);
