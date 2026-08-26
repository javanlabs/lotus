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

      rows.sort((a, b) => {
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
      });

      rows.forEach((r) => box.appendChild(r));
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
        if (cell) cell.textContent = ++n;
      });
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
