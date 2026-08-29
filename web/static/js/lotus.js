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

// lotusSelect drives web/templates/components/select.html — the styled
// replacement for a native <select>.
//
// The options are already in the DOM (server-rendered inside the panel);
// this reads them back rather than being handed a JSON copy, so there is
// one source of truth for what the choices are.
function lotusSelect(config) {
  return {
    open: false,
    label: "",
    // Positions are strings because they go straight into :style. The
    // panel is position:fixed, so these are viewport coordinates.
    pos: { top: "0px", left: "0px", width: "0px", maxHeight: "280px" },
    typed: "",
    typedAt: 0,

    init() {
      // $nextTick, not straight away: the panel is teleported into <body>
      // and does not exist yet while this component initializes. Reading
      // $refs.panel here threw, and the only visible symptom was a
      // trigger with no label — the options were fine all along.
      this.$nextTick(() => {
        this.options().forEach((opt, i) => {
          // Ids exist for aria-activedescendant, which is how a combobox
          // announces the highlighted option while focus stays on the
          // button.
          if (!opt.id) opt.id = `${this.$id("lotus-select")}-opt-${i}`;
        });
        // A native select with no selected option shows its first one and
        // submits that value. Match it, so a form that never touches the
        // control submits what the user was shown.
        if (!this.value() || !this.optionFor(this.value())) {
          const first = this.options().find((o) => !this.isDisabled(o));
          if (first) this.$refs.input.value = first.dataset.value;
        }
        this.syncLabel();
      });

      // The panel is fixed, so it does not travel with a scrolling
      // ancestor on its own. Capture phase catches scrolls of any
      // container, not just the window.
      this._reflow = () => {
        if (this.open) this.place();
      };
      window.addEventListener("scroll", this._reflow, true);
      window.addEventListener("resize", this._reflow);
    },

    destroy() {
      window.removeEventListener("scroll", this._reflow, true);
      window.removeEventListener("resize", this._reflow);
    },

    options() {
      const panel = this.$refs.panel;
      return panel ? Array.from(panel.querySelectorAll(".lotus-select-option")) : [];
    },

    isDisabled(opt) {
      return opt.dataset.disabled === "true";
    },

    value() {
      return this.$refs.input.value;
    },

    optionFor(value) {
      return this.options().find((o) => o.dataset.value === value);
    },

    isSelected(opt) {
      return opt.dataset.value === this.value();
    },

    syncLabel() {
      const opt = this.optionFor(this.value());
      this.label = opt ? opt.textContent.trim() : "";
    },

    toggle() {
      this.open ? this.close() : this.show();
    },

    show() {
      this.open = true;
      this.markActive(this.optionFor(this.value()) || this.options()[0]);
      // Measured after the panel is displayed: a hidden element has no
      // height to flip against.
      this.$nextTick(() => {
        this.place();
        const active = this.activeOption();
        if (active) active.scrollIntoView({ block: "nearest" });
      });
    },

    close() {
      if (!this.open) return;
      this.open = false;
      this.$refs.trigger.focus();
    },

    place() {
      const b = this.$refs.trigger.getBoundingClientRect();
      const panel = this.$refs.panel;
      const margin = 8;

      // Room below the trigger decides which way the list opens, and how
      // tall it may be — a list capped at a fixed height that then runs
      // off the screen is worse than a shorter scrolling one.
      const below = window.innerHeight - b.bottom - margin - 6;
      const above = b.top - margin - 6;
      const flip = below < Math.min(panel.scrollHeight, 200) && above > below;
      const maxHeight = Math.max(120, Math.min(280, flip ? above : below));

      this.pos = {
        top: `${Math.round(flip ? Math.max(margin, b.top - Math.min(panel.scrollHeight, maxHeight) - 6) : b.bottom + 6)}px`,
        left: `${Math.round(Math.max(margin, Math.min(b.left, window.innerWidth - b.width - margin)))}px`,
        width: `${Math.round(b.width)}px`,
        maxHeight: `${Math.round(maxHeight)}px`,
      };
    },

    activeOption() {
      return this.options().find((o) => o.dataset.active === "true");
    },

    markActive(opt) {
      if (!opt) return;
      this.options().forEach((o) => delete o.dataset.active);
      opt.dataset.active = "true";
      this.$refs.trigger.setAttribute("aria-activedescendant", opt.id);
    },

    move(step) {
      const opts = this.options().filter((o) => !this.isDisabled(o));
      if (!opts.length) return;
      const current = opts.indexOf(this.activeOption());
      const next = current < 0 ? 0 : (current + step + opts.length) % opts.length;
      this.markActive(opts[next]);
      opts[next].scrollIntoView({ block: "nearest" });
    },

    choose(opt) {
      if (this.isDisabled(opt)) return;
      const changed = opt.dataset.value !== this.value();
      this.$refs.input.value = opt.dataset.value;
      this.syncLabel();
      this.close();
      if (!changed) return;
      // A hidden input changed by script fires nothing on its own; anything
      // listening for a change on this field (htmx, another Alpine
      // component) would never hear about it otherwise.
      this.$refs.input.dispatchEvent(new Event("change", { bubbles: true }));
      if (config.autoSubmit) this.submit();
    },

    submit() {
      const form = this.$refs.input.form;
      if (!form) return;
      // requestSubmit runs validation and fires submit handlers, which
      // form.submit() skips — this form may have an onsubmit confirm.
      if (form.requestSubmit) form.requestSubmit();
      else form.submit();
    },

    onTriggerKey(event) {
      const key = event.key;
      if (!this.open) {
        if (key === "Enter" || key === " " || key === "ArrowDown" || key === "ArrowUp") {
          event.preventDefault();
          this.show();
        }
        return;
      }
      switch (key) {
        case "Escape":
          event.preventDefault();
          this.close();
          return;
        case "Enter":
        case " ":
          event.preventDefault();
          if (this.activeOption()) this.choose(this.activeOption());
          return;
        case "ArrowDown":
          event.preventDefault();
          this.move(1);
          return;
        case "ArrowUp":
          event.preventDefault();
          this.move(-1);
          return;
        case "Home":
        case "End": {
          event.preventDefault();
          const opts = this.options().filter((o) => !this.isDisabled(o));
          if (opts.length) this.markActive(key === "Home" ? opts[0] : opts[opts.length - 1]);
          return;
        }
        case "Tab":
          this.open = false;
          return;
      }
      if (key.length === 1) this.typeahead(key);
    },

    // Type-ahead: consecutive letters within a second accumulate, so
    // "vi" reaches Viewer without stopping at Vault. The native control
    // does this and people who use it notice when it is missing.
    typeahead(char) {
      const now = Date.now();
      this.typed = now - this.typedAt > 1000 ? char : this.typed + char;
      this.typedAt = now;
      const match = this.options().find(
        (o) => !this.isDisabled(o) && o.textContent.trim().toLowerCase().startsWith(this.typed.toLowerCase()),
      );
      if (match) {
        this.markActive(match);
        match.scrollIntoView({ block: "nearest" });
      }
    },
  };
}
