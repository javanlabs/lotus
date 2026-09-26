// lotusSort powers the rows table's client-side column sorting.
//
// Server-side order is alphabetical by name — the one order that does not
// move when the numbers do. Sorting by any numeric column is a click on
// its header, and never leaves the page: these tables are one project's
// components or one organization's projects, both small enough that
// re-querying to reorder would cost more than it buys.
//
// Columns are addressed by their index among a row's children, which is
// the same index the header cell occupies. That is what keeps header and
// body in step: there is no separate column registry to fall out of sync.
function lotusSort(headingLabel) {
  return {
    col: 1,
    dir: 1,
    kind: 'text',
    labels: {},
    kinds: {},
    label: '',

    init() {
      this.labels[1] = (headingLabel || 'name').toLowerCase();
      this.kinds[1] = 'text';
      this.retitle();
    },

    // The direction is described in the reader's own terms rather than as
    // an arrow or the word "descending". Every Lotus score is normalised
    // so that higher is healthier, which makes "worst first" exact — and
    // it answers the question someone sorting a health table is actually
    // asking. An arrow only says which way, never what that means, and
    // readers genuinely disagree on which way ▼ points.
    retitle() {
      const name = this.labels[this.col] || '';
      let how;
      if (this.kinds[this.col] === 'num') {
        how = this.dir > 0 ? 'worst first' : 'best first';
      } else {
        how = this.dir > 0 ? 'A–Z' : 'Z–A';
      }
      this.label = 'sorted by ' + name + ' — ' + how;
    },

    sortBy(col, kind, text) {
      this.labels[col] = (text || '').toLowerCase();
      this.kinds[col] = kind;
      if (this.col === col) {
        this.dir = -this.dir;
      } else {
        this.col = col;
        this.kind = kind;
        // Every Lotus score is normalised so that higher is healthier,
        // which makes ASCENDING the worst-first direction. A numeric
        // column opens that way: someone sorting a health table is
        // looking for what needs attention, not what is already fine.
        this.dir = 1;
      }
      this.apply();
    },

    // In the header the arrow is only a marker for WHICH column is
    // active — the sentence above the table carries what the direction
    // means. Kept subtle and paired with a spelled-out title attribute.
    arrow(col) {
      if (this.col !== col) return '';
      return this.dir > 0 ? ' ↑' : ' ↓';
    },

    hint(col) {
      if (this.col !== col) return 'Sort by this column';
      return 'Sorted — click to reverse';
    },

    apply() {
      const box = this.$refs.rows;
      if (!box) return;
      const rows = Array.from(box.children);
      const keyOf = (row) => {
        const cell = row.children[this.col];
        return cell ? (cell.dataset.sort || '') : '';
      };
      const compare = (a, b) => {
        const av = keyOf(a), bv = keyOf(b);
        // A cell with no value ("no data", or a status-only kind) sorts
        // last in BOTH directions. Treating it as zero would rank
        // unmeasured repositories as the worst ones, which is a different
        // claim entirely.
        const aEmpty = av === '', bEmpty = bv === '';
        if (aEmpty !== bEmpty) return aEmpty ? 1 : -1;
        if (aEmpty) return 0;
        if (this.kind === 'num') return (parseFloat(av) - parseFloat(bv)) * this.dir;
        return av.localeCompare(bv, undefined, { sensitivity: 'base' }) * this.dir;
      };

      // On the folder tree a row sorts among its siblings only, and each
      // folder's rows stay directly beneath it — a flat sort by score
      // would scatter projects away from the folders that hold them.
      // Folders stay ahead of projects at every level.
      let ordered;
      if (rows.some((r) => r.dataset.node)) {
        const byParent = new Map();
        rows.forEach((r) => {
          const p = r.dataset.parent || '';
          if (!byParent.has(p)) byParent.set(p, []);
          byParent.get(p).push(r);
        });
        ordered = [];
        const seen = new Set();
        const walk = (parent) => {
          const kids = (byParent.get(parent) || []).slice().sort((a, b) => {
            const af = a.dataset.folder === '1', bf = b.dataset.folder === '1';
            if (af !== bf) return af ? -1 : 1;
            return compare(a, b);
          });
          kids.forEach((k) => {
            if (seen.has(k)) return;
            seen.add(k);
            ordered.push(k);
            if (k.dataset.folder === '1') walk(k.dataset.node);
          });
        };
        walk('');
      } else {
        ordered = rows.sort(compare);
      }

      ordered.forEach((r) => box.appendChild(r));
      this.retitle();
      this.renumber();
    },

    // Numbering follows what is on screen, so a filtered table reads
    // 1..n rather than keeping the gaps of the rows it is hiding.
    renumber() {
      const box = this.$refs.rows;
      if (!box) return;
      let n = 0;
      Array.from(box.children).forEach((row) => {
        if (row.offsetParent === null) return;
        const cell = row.children[0];
        // A selectable row's first cell is its checkbox, not a number.
        if (cell && !('noNumber' in cell.dataset)) cell.textContent = ++n;
      });
    },
  };
}

// lotusTree is the organization dashboard's page state: the Failing filter
// (read by dashboard_block's rows) and which folders are collapsed.
//
// Collapsed folders are remembered per browser. Storage can be missing or
// throw — a private window, blocked site data — so every access is guarded:
// the tree then starts expanded and collapsing still works, it just is not
// remembered.
const LOTUS_TREE_KEY = 'lotus.tree.collapsed';

function lotusTree() {
  return {
    filter: 'all',
    collapsed: {},

    init() {
      try {
        const saved = JSON.parse(window.localStorage.getItem(LOTUS_TREE_KEY) || '{}');
        if (saved && typeof saved === 'object') this.collapsed = saved;
      } catch (e) {
        this.collapsed = {};
      }
    },

    isCollapsed(id) {
      return !!this.collapsed[id];
    },

    toggle(id) {
      const next = { ...this.collapsed };
      if (next[id]) delete next[id];
      else next[id] = true;
      this.collapsed = next;
      try {
        window.localStorage.setItem(LOTUS_TREE_KEY, JSON.stringify(next));
      } catch (e) {
        // Not remembered; the collapse itself still happened.
      }
    },

    // hiddenBy: is any of these space-separated folder ids collapsed?
    hiddenBy(ancestors) {
      return (ancestors || '').split(' ').some((id) => id && this.collapsed[id]);
    },

    // Multi-select for the bulk move. `selected` is a set of project ids,
    // replaced (not mutated) on every change so Alpine re-renders the
    // checkboxes, the count and the move form's hidden inputs. It lives only
    // for the page: a reload after a move clears it.
    selected: {},

    isSelected(id) {
      return !!this.selected[id];
    },

    toggleSelect(id) {
      const next = { ...this.selected };
      if (next[id]) delete next[id];
      else next[id] = true;
      this.selected = next;
    },

    selectedIds() {
      return Object.keys(this.selected);
    },

    selectedCount() {
      return this.selectedIds().length;
    },

    clearSelection() {
      this.selected = {};
    },

    // The header checkbox acts on the project rows currently on screen —
    // not ones hidden by a collapsed folder or the Failing filter.
    visibleProjectIds(box) {
      if (!box) return [];
      return Array.from(box.querySelectorAll('[data-project-select]'))
        .filter((cb) => cb.offsetParent !== null)
        .map((cb) => cb.dataset.id);
    },

    selectVisible(box) {
      const next = { ...this.selected };
      this.visibleProjectIds(box).forEach((id) => { next[id] = true; });
      this.selected = next;
    },

    allVisibleSelected(box) {
      const ids = this.visibleProjectIds(box);
      return ids.length > 0 && ids.every((id) => this.selected[id]);
    },
  };
}

// lotusInfo drives the little "how is this number calculated" popovers on
// the metric cards.
//
// The panel is position:fixed and placed from the button's own
// getBoundingClientRect, rather than absolute inside the card. The card
// strip scrolls horizontally (overflow-x:auto), and an absolutely
// positioned child of a scroll container is clipped by it — the reason an
// earlier attempt rendered the explanation inline below the strip, which
// pushed the page around every time it opened. Fixed positioning is
// measured against the viewport, so it escapes the clip entirely and
// nothing under it moves.
function lotusInfo() {
  return {
    key: null,
    top: 0,
    left: 0,

    toggle(key, event) {
      if (this.key === key) {
        this.key = null;
        return;
      }
      // Measure the button NOW, not inside $nextTick: the browser clears
      // event.currentTarget once dispatch finishes, so reading it later
      // yields null and the panel lands at 0,0 over the sidebar.
      const anchor = event.currentTarget.getBoundingClientRect();
      this.key = key;
      this.$nextTick(() => this.place(anchor));
    },

    place(b) {
      const panel = this.$refs.panel;
      if (!panel) return;
      const p = panel.getBoundingClientRect();
      const margin = 8;

      // Prefer directly below the button, flipping above when that would
      // run off the bottom of the viewport.
      let top = b.bottom + 6;
      if (top + p.height > window.innerHeight - margin) {
        top = Math.max(margin, b.top - p.height - 6);
      }
      // Left-aligned to the button, pulled back in if it would overflow.
      let left = b.left;
      if (left + p.width > window.innerWidth - margin) {
        left = Math.max(margin, window.innerWidth - p.width - margin);
      }
      this.top = Math.round(top);
      this.left = Math.round(left);
    },

    close() {
      this.key = null;
    },
  };
}

// The single entry point for success messages, wrapping Basecoat's
// toaster (web/static/vendor/basecoat-toast.min.js).
//
// Two things feed it: the app shell seeds a server-side flash as data
// attributes on the toaster (the mutation-then-redirect path, see
// internal/web/flash.go), and an htmx HX-Trigger event calls it at runtime
// for actions that do not reload. Keeping one function in front of
// Basecoat means callers still say lotusToast({tone, text}) in Lotus's own
// vocabulary, and swapping the toast implementation again would not touch
// them.
(() => {
  // Lotus tones are the status vocabulary used everywhere else in the
  // product; Basecoat categorises differently. Map at the boundary.
  const CATEGORY = { pass: "success", warn: "warning", fail: "error", none: "info" };

  // Basecoat attaches .toast() when it initializes the element, which may
  // be after this file runs. Anything raised before then waits here rather
  // than being dropped.
  let pending = [];

  // Basecoat inserts the toast's title with innerHTML rather than as text,
  // so anything reaching deliver() is escaped here — the single boundary
  // both callers (the seeded flash cookie and an htmx HX-Trigger event)
  // pass through — before it is handed over. Without this, a folder (or
  // anything else) named with markup runs script in whoever's browser
  // shows the resulting "Folder %s moved/deleted/created/renamed" toast.
  const ESCAPE_HTML = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  const escapeHtml = (s) => String(s).replace(/[&<>"']/g, (ch) => ESCAPE_HTML[ch]);

  const deliver = (msg) => {
    const toaster = document.getElementById("toaster");
    if (!toaster || typeof toaster.toast !== "function") return false;
    toaster.toast({
      category: CATEGORY[msg.tone] || "info",
      title: escapeHtml(msg.text),
      // A failure stays until dismissed: a message saying something went
      // wrong should not vanish while the reader is still working out what
      // to do about it.
      duration: msg.tone === "fail" ? -1 : 5000,
      cancel: msg.tone === "fail" ? { label: "Dismiss" } : undefined,
    });
    return true;
  };

  window.lotusToast = (msg) => {
    if (!msg || !msg.text) return;
    if (!deliver(msg)) pending.push(msg);
  };

  const flush = () => {
    const queued = pending;
    pending = [];
    queued.forEach((msg) => window.lotusToast(msg));
  };

  const seedFromFlash = () => {
    const toaster = document.getElementById("toaster");
    if (!toaster) return;
    const text = toaster.dataset.flashText;
    if (!text) return;
    // Clear it first: this runs on both DOMContentLoaded and Basecoat's
    // init, and the flash must be shown once, not once per listener.
    delete toaster.dataset.flashText;
    window.lotusToast({ tone: toaster.dataset.flashTone || "pass", text });
  };

  // Basecoat dispatches basecoat:initialized ON THE COMPONENT ELEMENT, and
  // a CustomEvent does not bubble unless asked to — so a document-level
  // listener for it never fires. Bind it to the toaster itself, and only
  // once the element exists.
  const wire = () => {
    const toaster = document.getElementById("toaster");
    if (!toaster) return;
    const ready = () => {
      flush();
      seedFromFlash();
    };
    if (typeof toaster.toast === "function") {
      ready();
    } else {
      // basecoat.min.js is deferred, so it has usually initialized by
      // DOMContentLoaded; this covers the other order, and the case where
      // its MutationObserver gets to the element later.
      toaster.addEventListener("basecoat:initialized", ready, { once: true });
    }
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", wire);
  } else {
    wire();
  }

  // htmx sends HX-Trigger as an event on <body>; detail carries the JSON
  // value the header was given.
  document.addEventListener("lotus:toast", (e) => {
    if (e.detail) window.lotusToast(e.detail);
  });
})();

// lotusModal drives web/templates/components/modal.html.
//
// The heavy lifting is the browser's: <dialog>.showModal() gives a focus
// trap, Esc-to-close, a ::backdrop and top-layer stacking for free. This
// only wires the trigger, keeps the body's scrollbar from double-scrolling
// behind the dialog, and listens for the close event htmx fires after a
// successful submit.
function lotusModal(name) {
  return {
    open() {
      const dlg = this.$refs.dialog;
      if (!dlg || dlg.open) return;
      dlg.showModal();
      document.body.style.overflow = "hidden";
    },

    close() {
      const dlg = this.$refs.dialog;
      if (dlg && dlg.open) dlg.close();
      document.body.style.overflow = "";
    },

    init() {
      // Esc and backdrop dismissal go through the browser, not through
      // us, so the scroll lock is released on the dialog's own close
      // event rather than only in close().
      this.$refs.dialog.addEventListener("close", () => {
        document.body.style.overflow = "";
      });

      // Clicking the backdrop: a <dialog>'s own box is the panel, so a
      // click whose target is the dialog itself landed outside it.
      this.$refs.dialog.addEventListener("click", (e) => {
        if (e.target === this.$refs.dialog) this.close();
      });

      document.body.addEventListener("lotus:modal-close", (e) => {
        if (!e.detail || !e.detail.name || e.detail.name === name) this.close();
      });
    },
  };
}

// htmx swaps DOM in and out without Alpine knowing about it, which breaks
// in both directions once a swapped fragment contains Alpine components:
// arriving components never initialize, and departing ones never clean up.
// Basecoat handles itself through a MutationObserver, but everything of
// ours that is still Alpine — anything with x-data inside a swapped
// fragment — needs these.
document.addEventListener("htmx:beforeSwap", (e) => {
  if (window.Alpine && Alpine.destroyTree && e.detail && e.detail.target) {
    Alpine.destroyTree(e.detail.target);
  }
});

// htmx:load fires for each newly added content root and hands it over as
// detail.elt, which is what initTree wants. Using it rather than
// afterSwap also covers outerHTML swaps, where the swap target itself is
// gone by the time the event fires.
//
// Bound to document, not document.body: this file is loaded in <head>
// without defer (it has to define the Alpine component functions before
// Alpine parses the page), so document.body is still null here.
document.addEventListener("htmx:load", (e) => {
  if (window.Alpine && Alpine.initTree && e.detail && e.detail.elt) {
    Alpine.initTree(e.detail.elt);
  }
});

// Two things Basecoat's combobox does not do, both delegated from document
// so they apply to every combobox including ones htmx swaps in later.
//
// 1. CLIPPING. Basecoat positions the list absolutely inside the
//    component, so any ancestor with an overflow clips it — the modal's
//    scrolling body, a card with overflow:hidden. Raising it into the top
//    layer with the Popover API escapes every ancestor at once, including
//    .lotus-card's backdrop-filter, which clips even position:fixed
//    descendants. Coordinates then have to come from us, since a top-layer
//    element is positioned against the viewport.
//
// 2. TOGGLING. handleInputClick in Basecoat's combobox always opens; a
//    second click on the input leaves the list open, which is not what a
//    select does anywhere else. root.close() is public, so this closes it.
(() => {
  const popoverOf = (root) => root.querySelector("[data-popover]");
  const isOpen = (pop) => pop && pop.getAttribute("aria-hidden") === "false";

  // Width and horizontal position are set BEFORE the list is displayed.
  // Doing it afterwards meant the first open painted once at its natural
  // width and only then snapped to the input's — visible as a wrong width
  // on the first open and never again, because the inline style persisted.
  const presize = (root) => {
    const pop = popoverOf(root);
    const input = root.querySelector('input[role="combobox"]');
    if (!pop || !input) return;
    const b = input.getBoundingClientRect();
    pop.style.minWidth = `${Math.round(b.width)}px`;
    pop.style.left = `${Math.round(b.left)}px`;
    pop.style.top = `${Math.round(b.bottom + 4)}px`;
  };

  // Then placed below the input, or above it when there is not enough
  // room — which can only be decided once it has been displayed, since a
  // hidden element has no height to measure.
  const place = (root) => {
    const pop = popoverOf(root);
    const input = root.querySelector('input[role="combobox"]');
    if (!pop || !input) return;
    const b = input.getBoundingClientRect();
    const gap = 4;
    const below = window.innerHeight - b.bottom - gap;
    const above = b.top - gap;
    const height = Math.min(pop.scrollHeight, 280);
    const flip = below < height && above > below;

    pop.style.minWidth = `${Math.round(b.width)}px`;
    pop.style.left = `${Math.round(b.left)}px`;
    pop.style.top = flip
      ? `${Math.round(Math.max(gap, b.top - height - gap))}px`
      : `${Math.round(b.bottom + gap)}px`;
    // The listbox is the scroll container, not the panel — the panel has
    // to stay unclipped so its ring frames the list.
    const list = pop.querySelector('[role="listbox"]');
    if (list) list.style.maxHeight = `${Math.round(Math.max(96, (flip ? above : below) - 12))}px`;
    pop.dataset.lotusPlaced = "true";
  };

  const show = (pop) => {
    if (typeof pop.showPopover !== "function") return; // older engine: leave Basecoat's absolute list alone
    if (!pop.matches(":popover-open")) {
      try {
        pop.showPopover();
      } catch {
        /* already shown, or not promotable */
      }
    }
  };
  const hide = (pop) => {
    if (typeof pop.hidePopover !== "function") return;
    if (pop.matches(":popover-open")) {
      try {
        pop.hidePopover();
      } catch {
        /* already hidden */
      }
    }
    delete pop.dataset.lotusPlaced;
  };

  // Basecoat signals state through aria-hidden on the list, so that is what
  // we watch rather than trying to intercept every path that opens it.
  const observer = new MutationObserver((records) => {
    for (const r of records) {
      const pop = r.target;
      if (!pop.matches || !pop.matches("[data-popover]")) continue;
      const root = pop.closest(".lotus-combobox");
      if (!root) continue;
      if (isOpen(pop)) {
        presize(root);
        show(pop);
        place(root);
      } else {
        hide(pop);
      }
    }
  });

  const start = () => {
    observer.observe(document.body, {
      subtree: true,
      attributes: true,
      attributeFilter: ["aria-hidden"],
    });
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }

  // A top-layer element does not travel with a scrolling ancestor, so it is
  // repositioned rather than left behind. Capture phase catches scrolls of
  // any container, not just the window.
  const reflow = () => {
    document.querySelectorAll(".lotus-combobox").forEach((root) => {
      if (isOpen(popoverOf(root))) place(root);
    });
  };
  window.addEventListener("scroll", reflow, true);
  window.addEventListener("resize", reflow);

  // Two clicks reach the input and they need opposite outcomes:
  //
  //   - a real second click on the input, which should close the list;
  //   - a phantom click that trails choosing an option — selecting closes
  //     the list, then a click lands on the input and Basecoat's
  //     handleInputClick springs it back open.
  //
  // They are told apart by where the gesture STARTED. A real click was
  // preceded by a mousedown on the input; the phantom one began on the
  // option. A time window was tried first and was wrong: it also swallowed
  // a genuine reopen right after choosing.
  let downOnInput = false;
  let wasOpen = false;
  document.addEventListener(
    "mousedown",
    (e) => {
      const input = e.target.closest?.('.lotus-combobox input[role="combobox"]');
      downOnInput = !!input;
      wasOpen = downOnInput && isOpen(popoverOf(input.closest(".lotus-combobox")));
    },
    true,
  );
  document.addEventListener("click", (e) => {
    const input = e.target.closest?.('.lotus-combobox input[role="combobox"]');
    if (!input) return;
    const root = input.closest(".lotus-combobox");
    if (!root || typeof root.close !== "function") return;
    // Runs after Basecoat's own handler, so this closes what it just opened.
    if (!downOnInput || wasOpen) root.close(downOnInput);
    downOnInput = false;
    wasOpen = false;
  });
})();

// sparklines.js scans for [data-sparkline="true"] exactly once, when the
// script runs. Every sparkline in Lotus arrives after that, lazily over
// htmx once its row is on screen, so it would never be drawn. This
// initializes the ones htmx brings in.
//
// initSparkline is a top-level const in a classic script, which puts it in
// the global lexical scope rather than on window — reachable by name from
// here, but only while that file is loaded as a classic script.
document.addEventListener("htmx:load", (e) => {
  const root = e.detail && e.detail.elt;
  if (!root || typeof initSparkline !== "function") return;
  const charts = root.matches?.('[data-sparkline="true"]')
    ? [root]
    : root.querySelectorAll?.('[data-sparkline="true"]') || [];
  charts.forEach((chart) => {
    // The library has no idea whether it has already drawn an element, and
    // drawing twice stacks two polylines in the same <svg>.
    if (chart.dataset.lotusSparklineDrawn) return;
    chart.dataset.lotusSparklineDrawn = "true";
    initSparkline(chart);
  });
});
