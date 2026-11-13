const SORTABLE_MOVE_MS = 180;
const SORTABLE_EASING = "cubic-bezier(0.2, 0, 0, 1)";
const SORTABLE_DRAG_THRESHOLD_PX = 4;
const SORTABLE_EDGE_PX = 40;
const SORTABLE_SCROLL_STEP_PX = 14;

function sortableDuration() { return matchMedia("(prefers-reduced-motion: reduce)").matches ? 0 : SORTABLE_MOVE_MS; }

function makeSortable(list, handleSelector, moved) {
  let drag = null;
  let settling = false;

  const shiftItems = ({ items, item, from, to, size }) => {
    items.forEach((other, index) => {
      if (other === item) return;
      const shift = index > from && index <= to ? -size : index < from && index >= to ? size : 0;
      other.style.transform = shift ? `translateY(${shift}px)` : "";
    });
  };

  const follow = () => {
    const { bounds, from } = drag;
    const own = bounds[from];
    const offset = drag.y - drag.startY + list.scrollTop - drag.startScroll;
    drag.offset = Math.min(Math.max(offset, bounds[0].top - own.top), bounds[bounds.length - 1].bottom - own.bottom);
    const center = (own.top + own.bottom) / 2 + drag.offset;
    drag.to = bounds.filter((box, index) => index !== from && center > (box.top + box.bottom) / 2).length;
    drag.item.style.transform = `translateY(${drag.offset}px)`;
    shiftItems(drag);
  };

  const autoScroll = () => {
    const box = list.getBoundingClientRect();
    const above = box.top + SORTABLE_EDGE_PX - drag.y, below = drag.y - box.bottom + SORTABLE_EDGE_PX;
    const speed = above > 0 ? -Math.min(above / SORTABLE_EDGE_PX, 1) : below > 0 ? Math.min(below / SORTABLE_EDGE_PX, 1) : 0;
    const scrollTop = list.scrollTop;
    list.scrollTop += speed * SORTABLE_SCROLL_STEP_PX;
    if (list.scrollTop !== scrollTop) follow();
    drag.frame = requestAnimationFrame(autoScroll);
  };

  const finish = (to) => {
    const settled = { ...drag, to };
    drag = null;
    cancelAnimationFrame(settled.frame);
    if (!settled.active) return;
    settling = true;
    shiftItems(settled);
    const { items, item, bounds, from, offset } = settled;
    const target = to > from ? bounds[to].bottom - bounds[from].bottom : bounds[to].top - bounds[from].top;
    item.style.transform = `translateY(${target}px)`;
    item.animate([{ transform: `translateY(${offset}px)` }, { transform: `translateY(${target}px)` }], {
      duration: sortableDuration(),
      easing: SORTABLE_EASING
    })
      .finished.then(() => {
        // Without transitions, clearing the shifts and reordering look the same.
        list.classList.remove("sorting");
        item.classList.remove("dragging");
        for (const other of items) other.style.transform = "";
        // Rendering new items while this one settled leaves nothing to reorder.
        if (to !== from && item.parentNode === list) {
          list.insertBefore(item, to > from ? items[to].nextSibling : items[to]);
          moved(item);
        }
        settling = false;
      });
  };

  list.addEventListener("pointerdown", (event) => {
    const handle = event.target.closest(handleSelector);
    if (!handle || event.button !== 0 || drag || settling) return;
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const items = [...list.children];
    const item = items.find((child) => child.contains(handle));
    const bounds = items.map((child) => child.getBoundingClientRect());
    const from = items.indexOf(item);
    drag = {
      pointerId: event.pointerId,
      items,
      item,
      bounds,
      from,
      to: from,
      size: bounds[from].height + parseFloat(getComputedStyle(list).rowGap || 0),
      startY: event.clientY,
      y: event.clientY,
      startScroll: list.scrollTop,
      offset: 0,
      active: false,
      frame: 0
    };
  });
  list.addEventListener("pointermove", (event) => {
    if (drag?.pointerId !== event.pointerId) return;
    drag.y = event.clientY;
    if (!drag.active) {
      if (Math.abs(drag.y - drag.startY) < SORTABLE_DRAG_THRESHOLD_PX) return;
      drag.active = true;
      list.classList.add("sorting");
      drag.item.classList.add("dragging");
      drag.frame = requestAnimationFrame(autoScroll);
    }
    follow();
  });
  list.addEventListener("pointerup", (event) => { if (drag?.pointerId === event.pointerId) finish(drag.to); });
  list.addEventListener("pointercancel", (event) => { if (drag?.pointerId === event.pointerId) finish(drag.from); });
  // Capture Escape before a surrounding dialog treats it as a close request.
  addEventListener(
    "keydown",
    (event) => {
      if (event.key !== "Escape" || !drag?.active) return;
      event.preventDefault();
      event.stopPropagation();
      finish(drag.from);
    },
    true
  );
  list.addEventListener("keydown", (event) => {
    const handle = event.target.closest(handleSelector);
    if (!handle || drag || settling) return;
    const item = [...list.children].find((child) => child.contains(handle));
    const neighbor = event.key === "ArrowUp" ? item.previousElementSibling : event.key === "ArrowDown" ? item.nextElementSibling : null;
    if (!neighbor) return;
    event.preventDefault();
    const pair = [item, neighbor];
    const tops = pair.map((child) => child.getBoundingClientRect().top);
    // Moving the neighbor keeps the focused handle in the document.
    if (event.key === "ArrowUp") item.after(neighbor); else item.before(neighbor);
    pair.forEach((child, index) => {
      const shift = tops[index] - child.getBoundingClientRect().top;
      child.animate([{ transform: `translateY(${shift}px)` }, { transform: "none" }], {
        duration: sortableDuration(),
        easing: SORTABLE_EASING
      });
    });
    item.scrollIntoView({ block: "nearest" });
    moved(item);
  });
}
