const tooltipContents = new WeakMap();

function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs || {})) {
    if (name === "class") node.className = value;
    else if (name === "text") setText(node, value);
    else if (name === "tooltip") {
      if (value) {
        tooltipContents.set(node, value);
        node.dataset.tip = "";
      }
    } else if (name.startsWith("on")) {
      node.addEventListener(name.slice(2), value);
    } else if (value !== null && value !== undefined && value !== false) setTextAttribute(node, name, value);
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child.nodeType ? child : window.billingI18n.textNode(child));
  }
  return node;
}

function metricTag(metrics, accessibleLabel) {
  return el(
    "span",
    { class: "tag plain metrics", "aria-label": accessibleLabel },
    metrics.flatMap(([label, value], index) => [
      index > 0 && el("span", { class: "tag-separator", text: DISPLAY_SEPARATOR, "aria-hidden": "true" }),
      el("span", { class: "tag-metric" }, el("span", { class: "tag-label", text: label }), el("span", { class: "tag-value", text: value }))
    ])
  );
}

// Replace a display collection while retaining its scroll position.
// Paged lists also use the returned row container to append new entries.
function renderCollection(target, items, { render, headers, attrs, selection, empty = m("ui.no_data") }) {
  if (selection) selection.update(target, items);
  const top = target.scrollTop, left = target.scrollLeft;
  const tableLeft = target.querySelector(":scope > .table-scroll")?.scrollLeft || 0;
  let content = el("div", { class: "empty", text: empty });
  let rows = null;
  if (items.length) {
    rows = el(
      headers ? "tbody" : "div",
      headers ? {} : attrs,
      items.map((item, index) => {
        const row = render(item, index);
        if (selection) row.prepend(el("td", { class: "selection-cell" }, selection.checkbox(item)));
        return row;
      })
    );
    content = headers
      ? el(
          "div",
          { class: "table-scroll" },
          el(
            "table",
            attrs,
            el(
              "thead",
              {},
              el(
                "tr",
                {},
                selection && el("th", { scope: "col", class: "selection-cell" }, selection.checkbox()),
                headers.map((h) => el("th", { scope: "col", class: [h.class, h.num ? "num" : ""].filter(Boolean).join(" ") }, h.label ?? h))
              )
            ),
            rows
          )
        )
      : rows;
  }
  target.replaceChildren(
    ...(selection && items.length
      ? [el("label", { class: "selection-mobile" }, selection.checkbox(), el("span", { text: m("ui.select_all_visible") }))]
      : []),
    content
  );
  if (selection) selection.sync();
  if (headers) content.scrollLeft = tableLeft;
  target.scrollTop = top;
  target.scrollLeft = left;
  refreshHoverTooltip();
  return rows;
}
let noticeTimer = 0;
function hideNotice() {
  clearTimeout(noticeTimer);
  const node = $("notice");
  node.className = "notice";
  if (node.hidePopover && node.matches(":popover-open")) node.hidePopover();
}

function notify(message, kind) {
  const node = $("notice");
  setText(node, message);
  node.className = "notice show " + (kind || "ok");
  if (node.showPopover && !node.matches(":popover-open")) { node.showPopover(); }
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(hideNotice, kind === "err" ? 9000 : 4000);
}
async function fail(err) {
  if (err instanceof StaleRequestError) return;
  if (err instanceof AuthError) {
    const role = currentRole || loginMode;
    const generation = sessionGeneration;
    await deleteCredential(role);
    if (generation === sessionGeneration) showGate(role, err.message);
    return;
  }
  notify(err.message || String(err), "err");
}
async function guard(fn) { try { await fn(); } catch (err) { await fail(err); } }

function openActionDialog(title, content, value, save, confirm = m("ui.confirm")) {
  return new Promise((resolve) => {
    let accepted = false;
    $("action-dialog").addEventListener("close", () => resolve(accepted), { once: true });
    openEditor("action-dialog", null, () => {
      setText($("action-dialog-title"), title);
      $("action-content").replaceChildren(content?.nodeType ? content : window.billingI18n.textNode(content));
      $("action-input").value = value || "";
      $("action-input").classList.toggle("hidden", value === undefined);
      setText($("action-confirm"), confirm);
      $("action-confirm").onclick = () =>
        submitEditor("action-dialog", async () => {
          await save?.($("action-input").value);
          accepted = true;
        });
    });
  });
}

const editors = new WeakMap();
function editorCurrent(editor) { return editor.dialog.open && editors.get(editor.dialog) === editor; }

function updateEditorControls(editor) {
  for (const button of editor.dialog.querySelectorAll(".dialog-actions button")) button.disabled = editor.saving;
  for (const field of editor.dialog.children) { if (!field.matches("h2, .editor-state, .dialog-actions")) field.inert = editor.saving; }
}

function openEditor(id, kind, initialize) {
  closeHoverTooltip();
  const dialog = $(id);
  const editor = {
    dialog,
    button: dialog.querySelector(".dialog-actions .primary"),
    notice: dialog.querySelector(".editor-state"),
    saving: false
  };
  editors.set(dialog, editor);
  try {
    editor.data = kind ? editorData(kind) : undefined;
    editor.initial = initialize(editor.data);
    editor.notice.className = "editor-state notice hidden";
    updateEditorControls(editor);
    dialog.showModal();
    dialog.querySelector("input:not([type=hidden]):not(.hidden):enabled")?.focus();
  } catch (error) { fail(error); }
}

async function submitEditor(id, save) {
  const editor = editors.get($(id));
  if (!editor || !editorCurrent(editor) || editor.saving) return;
  editor.saving = true;
  const label = window.billingI18n.boundText(editor.button);
  updateEditorControls(editor);
  setText(editor.button, m("ui.saving"));
  editor.notice.className = "editor-state notice hidden";
  try {
    const saved = await save(editor);
    if (saved !== false && editorCurrent(editor)) editor.dialog.close();
  } catch (error) {
    if (error instanceof AuthError || error instanceof StaleRequestError) return fail(error);
    if (editorCurrent(editor)) {
      editor.notice.className = "editor-state notice err show";
      setText(editor.notice, error.message || String(error));
    }
  } finally {
    editor.saving = false;
    if (editors.get(editor.dialog) === editor) {
      updateEditorControls(editor);
      setText(editor.button, label);
    }
  }
}

for (const id of [
  "plan-dialog",
  "route-dialog",
  "route-picker",
  "price-dialog",
  "action-dialog",
  "request-event-columns-dialog",
  "credential-dialog"
]) {
  const dialog = $(id);
  dialog.querySelector(".dialog-actions button:not(.primary):not(.link)").onclick = () => dialog.close();
  dialog.addEventListener("cancel", (event) => { if (editors.get(dialog)?.saving) event.preventDefault(); });
  dialog.addEventListener("close", () => { if (!dialog.open) editors.delete(dialog); });
}

// @license Lucide Icons - ISC; SVG assets are embedded locally.
const LUCIDE_ICONS = {
  "info": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>`,
  "pencil": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.5z"/><path d="m15 5 4 4"/></svg>`,
  "circle-check": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-circle-check"><circle cx="12" cy="12" r="10"/><path d="m16 9-5.5 5.5L8 12"/></svg>`,
  "route": `<svg class="lucide lucide-route" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="19" r="3"/><path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"/><circle cx="18" cy="5" r="3"/></svg>`,
  "astroid": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-astroid"><path d="M12.983 21.186a1 1 0 0 1-1.966 0 10 10 0 0 0-8.203-8.203 1 1 0 0 1 0-1.966 10 10 0 0 0 8.203-8.203 1 1 0 0 1 1.966 0 10 10 0 0 0 8.203 8.203 1 1 0 0 1 0 1.966 10 10 0 0 0-8.203 8.203"/></svg>`,
  x: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-x"><path d="M18 6 6 18M6 6l12 12"/></svg>`,
  refresh: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-rotate-cw"><path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>`,
  sun: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-sun"><circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/></svg>`,
  "sun-moon": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-sun-moon"><path d="M12 2v2"/><path d="M14.837 16.385a6 6 0 1 1-7.223-7.222c.624-.147.97.66.715 1.248a4 4 0 0 0 5.26 5.259c.589-.255 1.396.09 1.248.715"/><path d="M16 12a4 4 0 0 0-4-4"/><path d="m19 5-1.256 1.256"/><path d="M20 12h2"/></svg>`,
  moon: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-moon"><path d="M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401"/></svg>`,
  logout: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-log-out"><path d="m16 17 5-5-5-5"/><path d="M21 12H9"/><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/></svg>`,
  download: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-download"><path d="M12 15V3"/><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m7 10 5 5 5-5"/></svg>`,
  "arrow-left-right": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-arrow-left-right"><path d="M8 3 4 7l4 4"/><path d="M4 7h16"/><path d="m16 21 4-4-4-4"/><path d="M20 17H4"/></svg>`,
  copy: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-copy"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`,
  check: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-check"><path d="M20 6 9 17l-5-5"/></svg>`,
  chevron: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-chevron-right"><path d="m9 18 6-6-6-6"/></svg>`,
  "chevron-down": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-chevron-down"><path d="m6 9 6 6 6-6"/></svg>`,
  globe: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-globe"><circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/></svg>`,
  clock: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-clock"><circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/></svg>`,
  timer: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-timer"><line x1="10" x2="14" y1="2" y2="2"/><line x1="12" x2="15" y1="14" y2="11"/><circle cx="12" cy="14" r="8"/></svg>`,
  eye: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-eye"><path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/></svg>`,
  "eye-off": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-eye-off"><path d="M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49"/><path d="M14.084 14.158a3 3 0 0 1-4.242-4.242"/><path d="M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143"/><path d="m2 2 20 20"/></svg>`,
  key: `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-key-round"><path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5" fill="currentColor"/></svg>`,
  "rotate-cw-clock": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-rotate-cw-clock preview-icon"><path d="M12 7v5l4 2"/><path d="M16 8h5V3"/><path d="m21 8-2.3-2.3A9.7 9.7 0 0012 3a9 9 0 109 9"/></svg>`,
  "file-key": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-file-key-icon lucide-file-key"><path d="M14 2v5a1 1 0 0 0 1 1h5"/><path d="M4 12v6"/><path d="M4 14h2"/><path d="M9.65 22H18a2 2 0 0 0 2-2V8a2.4 2.4 0 0 0-.706-1.706l-3.588-3.588A2.4 2.4 0 0 0 14 2H6a2 2 0 0 0-2 2v4"/><circle cx="4" cy="20" r="2"/></svg>`,
  "move-right": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-move-right"><path d="M18 8L22 12L18 16"/><path d="M2 12H22"/></svg>`,
  "corner-down-right": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-corner-down-right"><path d="m15 10 5 5-5 5"/><path d="M4 4v7a4 4 0 0 0 4 4h12"/></svg>`,
  "corner-left-up": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-corner-left-up"><path d="M14 9 9 4 4 9"/><path d="M20 20h-7a4 4 0 0 1-4-4V4"/></svg>`,
  "equal-not": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-equal-not"><line x1="5" x2="19" y1="9" y2="9"/><line x1="5" x2="19" y1="15" y2="15"/><line x1="19" x2="5" y1="5" y2="19"/></svg>`,
  "square-text": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-square-text"><rect width="18" height="18" x="3" y="3" rx="2"/><path d="M7 8h8"/><path d="M7 12h10"/><path d="M7 16h6"/></svg>`,
  "circle-dollar-sign": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-circle-dollar-sign"><circle cx="12" cy="12" r="10"/><path d="M16 8h-6a2 2 0 1 0 0 4h4a2 2 0 1 1 0 4H8"/><path d="M12 18V6"/></svg>`,
  "columns-3-cog": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-columns-3-cog"><path d="M10.6 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v5.6"/><path d="m14.305 19.53.923-.382"/><path d="M15 3v7.6"/><path d="m15.229 16.852-.924-.383"/><path d="m16.852 15.228-.383-.923"/><path d="m16.852 20.772-.383.924"/><path d="m19.148 15.228.383-.923"/><path d="m19.53 21.696-.382-.924"/><path d="m20.773 16.852.922-.383"/><path d="m20.773 19.148.922.383"/><path d="M9 3v18"/><circle cx="18" cy="18" r="3"/></svg>`,
  "grip-vertical": `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-grip-vertical"><circle cx="9" cy="12" r="1"/><circle cx="9" cy="5" r="1"/><circle cx="9" cy="19" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="5" r="1"/><circle cx="15" cy="19" r="1"/></svg>`
};

function actionIcon(name, className = "") {
  const svg = iconSVG(LUCIDE_ICONS[name]);
  svg.classList.add("lucide-icon");
  if (name === "chevron") svg.classList.add("lucide-chevron-icon");
  if (className) svg.classList.add(...className.split(/\s+/).filter(Boolean));
  svg.setAttribute("aria-hidden", "true");
  return svg;
}

function setActionIcon(button, name, label) {
  button.replaceChildren(actionIcon(name));
  setTextAttribute(button, "aria-label", label);
  button.dataset.tip = "";
  refreshHoverTooltip();
}

function setLabeledActionIcon(button, name, label) {
  button.replaceChildren(actionIcon(name), el("span", { class: "labeled-icon-text" }, label));
  setTextAttribute(button, "aria-label", label);
}

function iconSVG(markup) {
  const template = document.createElement("template");
  template.innerHTML = markup;
  return template.content.firstElementChild;
}

async function copyText(text) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch (_) {}
  }
  // HTTP origins and embedded pages may lack clipboard access.
  const previousFocus = document.activeElement;
  const input = document.createElement("textarea");
  input.value = text;
  input.readOnly = true;
  input.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0";
  document.body.appendChild(input);
  try {
    input.focus({ preventScroll: true });
    input.select();
    if (!document.execCommand("copy")) throw new UIError();
  } catch (_) { throw new UIError(m("ui.copy_failed_check_clipboard_permissions")); } finally {
    input.remove();
    previousFocus?.focus({ preventScroll: true });
  }
}

// read may resolve the text asynchronously when the button is pressed.
function copyButton(label, read, attrs) {
  const button = el(
    "button",
    {
      class: "inline-icon-button link",
      type: "button",
      "data-tip": label,
      "aria-label": label,
      ...attrs,
      onclick: () =>
        guard(async () => {
          button.disabled = true;
          try {
            await copyText(await read());
            button.classList.add("copied");
            setActionIcon(button, "check", m("ui.copied"));
            setTimeout(() => {
              if (!button.isConnected) return;
              button.classList.remove("copied");
              setActionIcon(button, "copy", label);
            }, 2000);
          } finally { button.disabled = false; }
        })
    },
    actionIcon("copy")
  );
  return button;
}

let hoverTooltipAnchor = null;
let hoverTooltipPoint = null;
let hoverTooltipSource = "keyboard";
let hoverTooltipTimer = 0;
// Popover focus changes fire focusin synchronously.
let hoverTooltipTransitioning = false;

function closeHoverTooltip() {
  clearTimeout(hoverTooltipTimer);
  hoverTooltipTimer = 0;
  if (hoverTooltipAnchor) {
    const describedBy = (hoverTooltipAnchor.getAttribute("aria-describedby") || "").split(/\s+/)
      .filter((id) => id && id !== "hover-tooltip");
    if (describedBy.length) hoverTooltipAnchor.setAttribute("aria-describedby", describedBy.join(" "));
    else hoverTooltipAnchor.removeAttribute("aria-describedby");
  }
  hoverTooltipAnchor = null;
  const tooltip = $("hover-tooltip");
  if (tooltip.matches(":popover-open")) {
    hoverTooltipTransitioning = true;
    try { tooltip.hidePopover(); } finally { hoverTooltipTransitioning = false; }
  }
}

function scheduleHoverTooltipClose(next) {
  const tooltip = $("hover-tooltip");
  if (next && (tooltip.contains(next) || hoverTooltipAnchor?.contains(next))) return;
  if (hoverTooltipSource === "touch") return;
  clearTimeout(hoverTooltipTimer);
  if (next?.closest?.("button, input, select, textarea, a[href]")) closeHoverTooltip();
  else hoverTooltipTimer = setTimeout(closeHoverTooltip, 150);
}

function hoverTooltipProps(content, enabled = Boolean(content)) {
  return { tabindex: enabled ? "0" : null, tooltip: enabled ? content : null };
}

function refreshHoverTooltip() {
  if (!hoverTooltipAnchor) return;
  const anchor = hoverTooltipAnchor.isConnected
    ? hoverTooltipAnchor
    : hoverTooltipPoint && document.elementFromPoint(hoverTooltipPoint.x, hoverTooltipPoint.y)?.closest("[data-tip]");
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
  if (anchor === hoverTooltipAnchor) {
    clearTimeout(hoverTooltipTimer);
    return;
  }
  if (anchor) showHoverTooltip(anchor, { point: { x: event.clientX, y: event.clientY }, source: "mouse" });
  else scheduleHoverTooltipClose(event.target);
});
document.addEventListener("pointerout", (event) => { if (event.pointerType !== "touch") scheduleHoverTooltipClose(event.relatedTarget); });
document.addEventListener("focusin", (event) => {
  if ($("hover-tooltip").contains(event.target)) {
    clearTimeout(hoverTooltipTimer);
    return;
  }
  const anchor = event.target.closest("[data-tip]");
  if (anchor && !hoverTooltipTransitioning) {
    queueMicrotask(() => {
      if (anchor.contains(document.activeElement) && !(anchor === hoverTooltipAnchor && hoverTooltipSource === "touch"))
        showHoverTooltip(anchor);
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

function positionAnchoredPopover(node, anchor, { align = "start", side = "bottom", gap = 6, maxHeight = "80dvh" } = {}) {
  node.style.maxHeight = "";
  const trigger = anchor.getBoundingClientRect();
  const margin = 12, viewportWidth = document.documentElement.clientWidth;
  const below = Math.max(0, innerHeight - trigger.bottom - gap - margin);
  const above = Math.max(0, trigger.top - gap - margin);
  const preferredSpace = side === "top" ? above : below;
  const openAbove = node.offsetHeight <= preferredSpace ? side === "top" : above > below;
  node.style.maxHeight = "min(" + maxHeight + ", " + (openAbove ? above : below) + "px)";
  const top = openAbove ? trigger.top - gap - node.offsetHeight : trigger.bottom + gap;
  const left =
    align === "center"
      ? trigger.left + (trigger.width - node.offsetWidth) / 2
      : align === "end" ? trigger.right - node.offsetWidth : trigger.left;
  node.style.top = Math.max(margin, Math.min(top, innerHeight - node.offsetHeight - margin)) + "px";
  node.style.left = Math.max(margin, Math.min(left, viewportWidth - node.offsetWidth - margin)) + "px";
}

function switchControl({ input }) {
  return el("span", { class: "switch-control" }, input, el("span", { class: "switch-track", "aria-hidden": "true" }));
}

function checkboxControl({ input, state = "none" }) {
  const indicator = el(
    "span",
    { class: "checkbox-box", "data-state": state, "aria-hidden": "true" },
    actionIcon("check", "checkbox-check"),
    actionIcon("x", "checkbox-cross")
  );
  return input ? el("span", { class: "checkbox-control" }, input, indicator) : indicator;
}

// A chip and its tooltip must not share DOM nodes.
function chipText(value) {
  if (Array.isArray(value)) return value.map(chipText);
  if (value?.nodeType) return value.cloneNode(true);
  return displayText(window.billingI18n.isMessage(value) ? String(value) : value);
}

function settingsChipTooltip(item) {
  return el(
    "p",
    { class: "hover-tooltip-body settings-chip-tooltip mono" },
    item.iconName ? actionIcon(item.iconName) : null,
    el("span", { class: item.state === "deny" ? "routing-deny" : "" }, chipText(item.text)),
    item.tag ? el("span", { class: "tag warn", text: item.tag }) : null
  );
}

function chipTooltipProps(item, label) {
  return {
    tabindex: "-1",
    tooltip: () =>
      label.scrollWidth > label.clientWidth + 1
        ? [settingsChipTooltip(item)]
        : item.hint ? [el("div", { class: "hover-tooltip-body", text: displayLabel(item.hint) })] : null
  };
}

function settingsChipList(items, fallback) {
  if (!items.length) return el("span", { class: fallback === "—" ? "muted" : "", text: fallback });
  return el(
    "div",
    { class: "settings-chip-list" },
    items.map((item) => {
      const label = el("span", { class: "settings-chip-label" + (item.state === "deny" ? " routing-deny" : "") }, chipText(item.text));
      return el(
        "span",
        { class: "settings-chip" + (item.tag ? " has-tag" : ""), ...chipTooltipProps(item, label) },
        item.iconName ? actionIcon(item.iconName) : null,
        label,
        item.tag ? el("span", { class: "tag warn", text: item.tag }) : null
      );
    })
  );
}

function settingsEntryField(label, content) {
  return el(
    "div",
    { class: "settings-entry-field" },
    el("span", { class: "settings-entry-label", text: displayLabel(label) }),
    el("div", { class: "settings-entry-value" }, content)
  );
}

function settingsCard(title, fields, onEdit, onDelete) {
  return el(
    "article",
    { class: "inset-card settings-entry", role: "listitem" },
    el(
      "div",
      { class: "settings-entry-header" },
      el("div", { class: "settings-entry-title", text: displayLabel(title) }),
      el(
        "div",
        { class: "row settings-entry-actions" },
        el("button", { class: "link", text: m("ui.edit"), onclick: onEdit }),
        el("button", { class: "link danger", text: m("ui.delete"), onclick: onDelete })
      )
    ),
    el("div", { class: "settings-entry-fields" }, fields)
  );
}

function boundKeyField(keys) {
  const chips = keys.map((key) => ({ text: maskedText(apiKeyIdentity(key).text), tag: key.deleted_at ? m("ui.deleted") : "" }));
  return settingsEntryField(m("ui.bound_keys"), settingsChipList(chips, "—"));
}

function bindingSummary(keys) {
  const deleted = keys.filter((key) => key.deleted_at).length;
  return m("ui.value_api_keysvalue", { v0: keys.length, v1: deleted ? m("ui.value_deleted", { v0: deleted }) : "" });
}

function selectedKeyScopes(id) {
  return Array.from($(id).querySelectorAll('input[type="checkbox"]:checked:not(:disabled)'), (input) => input.value);
}

function sameScopes(left, right) { return JSON.stringify([...left].sort()) === JSON.stringify([...right].sort()); }

function renderDataPending(target, sources, message = m("ui.loading"), className = "empty") {
  const missing = sources.filter((source) => source.value === null);
  if (!missing.length) return false;
  // The page notice owns error details and retries for resource groups.
  target.replaceChildren(
    el("div", { class: className, text: missing.some((source) => source.error) ? m("ui.failed_to_load_data") : message })
  );
  return true;
}
