# Lotus Design System

Mirrored from the Claude Design project
`https://claude.ai/design/p/6fdfec68-c713-4ee1-9464-993ff1c177fc` ("Lotus Design System").
That project is the source of truth; this directory is a local copy of the parts that apply to
this codebase. Re-sync with the `DesignSync` tool (`/design-login` first).

Lotus is an internal software-health dashboard for **AlurKerja**, an Indonesian software team. It
collects the signals that are normally spread across six separate tools — GitLab pipelines, SonarQube
quality gates, Trivy image scans, Sentry error health, K6 load results and OWASP ZAP scans — and puts
them on one line per repository, with one score on top. The product's own line for itself is
**"every metric, surfaced"**.

---

## Content fundamentals

**Voice.** Flat, factual, engineer-to-engineer. Lotus reports; it does not advise, congratulate or
apologise. Sentences state a count, a ratio or a schedule and stop.

- Page subhead: "19 repositories across AlurKerja · every metric, surfaced"
- KPI note: "11 crit/high across 5 images"
- Form footer: "First collection runs within 10 minutes of creation."
- Deferred state: "Load and security test targets are set per project once the first collection completes."

**Person.** Almost none. Copy is impersonal — no "you", no "we", no "your repositories". The one place
a person appears is the account card, and only as a name and job title ("Rizky Hartono / Eng. Manager").

**Casing.** Sentence case for headings, labels and buttons ("Software health", "New project",
"Add repository"). ALL CAPS is reserved for monospace labels and eyebrows, and it is a *style*, not a
spelling — write "Project name", let the component uppercase it. Metric values are lowercase:
`clean`, `2 crit`, `3 MR idle`, `p95 610ms`, `stale 210d`.

**Density over prose.** A number plus one clarifying clause beats a sentence. Where a full sentence is
needed, it stays under about 110 characters.

**Punctuation.** The middle dot `·` separates peers in one line ("24 stale branches · 13 idle MRs",
"billing-prod · shared", "Trend · 14d"). An em dash is rare. `—` on its own means "no data".
Timestamps carry the timezone: "next collection 11:00 WIB", "Frozen at 08 Aug 2026, 10:42".

**Ellipsis** marks an action that opens something rather than doing it: "Share…", "Email to…".

**Terminology.** Tool names are used as-is and capitalised as the vendors do: GitLab, Sonar, Trivy,
Sentry, K6, ZAP. Domain nouns are project, repository, branch, quality gate, DSN, collection, score.
Never "repo" in UI copy (it appears only in mono counts, e.g. "2 repos").

**Emoji: never.** The only non-alphabetic glyphs allowed in copy are `▲` / `▼` for a delta, `▼` on a
picker affordance, `·` as a separator and `—` for missing data. Everything else is an SVG icon.

**Placeholders are real.** `Billing platform`, `alurkerja/billing-api`, `main` — never "Enter a value".

---

## Visual foundations

**Palette.** Two brand colours and three functional ones. Plum `#43284E` carries authority: headings,
the hero card, the single primary button, active navigation. Rose `#9E2B47` — the logo's centre petal —
is the secondary root: Blush is a white-tint scale of it (`#F7EEF0` → `#C68494`) and carries the page
bloom, the card underline and accents on plum, and Rose itself is the focus colour on every control
(`--focus-border` on the edge, `rgba(158,43,71,.18)` as the 3px ring). Teal `#23808F` means pass,
grey `#9CA3AF` means no data. Neutrals are a cool grey ramp (`#111827` → `#F9FAFB`) plus a
plum-tinted lilac set (`#F3EFF6`, `#F6F4F8`) used only for navigation states.

**Type.** IBM Plex Sans for everything a human reads; IBM Plex Mono for everything a machine produced —
labels, counts, repository paths, branch names, scores, timestamps, status chips. That split is the
single most recognisable thing about the product. Body is 12.5px; the scale steps down through 12,
11.5, 11, 10.5 to 9.5px mono labels. Headings are 700 with `-.02em` tracking; big numbers go to
`-.03em`. Mono labels are 600 with `.08–.12em` tracking and uppercase.

**The petal radius.** Containers use an asymmetric radius — large corner top-left and bottom-right,
small on the other diagonal (`16px 6px 16px 6px` down to `5px 2px 5px 2px`). It echoes the lotus petal
and appears on cards, menus, avatars and status chips. Controls are the deliberate exception: buttons,
nav rows, inputs and selects use plain symmetric radii, so dense chrome stays quiet. Within that
family the values are deliberate, not arbitrary: buttons and the segmented track share 8px so they
align in a page header, a segment thumb is 5px (concentric inside the track's 3px padding), inputs
are 8px, menu rows 7px, nav rows 6px.

**Cards.** Translucent white (`rgba(255,255,255,.72)`) over the page bloom with `blur(12px) saturate(1.1)`,
a 1px plum hairline border, and a four-part inset shadow: white top edge, a 4px page-coloured moat, a
1px plum ring, and a 2px blush underline at the bottom. Outer shadow is almost nothing
(`0 1px 1px` + a tight `0 4px 10px -8px`). The inverse card is solid plum with the same construction in
cream and blush. Cards never sit on cards.

**Backgrounds.** No photography, no illustration, no repeating pattern except one. The page is a
radial bloom: blush from the top-left, plum from the top-right, over `#F9FAFB`. The sidebar carries a
masked vertical wash of the same colours. The only texture in the system is the 135° 1px hatch behind
metric-table headers. Full-bleed imagery does not exist in this product.

**Buttons.** Two variants. Secondary is an embossed white gradient (`#FFF → #FAF9FB → #F1EFF4`) with a
plum label, machined rather than glossy: a crisp 1px hairline holds the shape and a solid unblurred
ledge sits beneath it, with no cast shadow and no highlight bloom; primary is a plum gradient
(`#5C3A69 → #46294F → #3A2143`) with cream text and blush icons. Hover darkens the gradient by one
step — never opacity, never a colour swap. Press collapses the ledge into an inset well, so the button
sinks in place. Nothing in the system translates.

**Hover / press elsewhere.** Nav rows and menu items fill with `--ink-100` (danger rows with blush).
Table rows fill with `--ink-50`. Icon-only destructive controls swap the glyph to rose on a blush fill.
There is no hover state on cards, chips or meters — they are read, not clicked.

**Focus.** One focus treatment system-wide, in the centre-petal rose: the element takes a
`--focus-border` edge where it has one, plus a 3px `--focus-ring` halo (`--shadow-focus-ring`) on top of
whatever shadow it already carries. Inputs, selects, buttons and the segmented track all use it, and
buttons and the track show it only on `:focus-visible` — keyboard, not mouse. Every input and select
shares one plain 8px radius, never the petal shape.

**Borders.** Three weights only: `--ink-200` hairlines between rows and inside menus, `--plum-a10`
around cards, `--plum-a16` around inputs. Dividers are 1px full-bleed or inset by 9px inside menus.

**Transparency and blur** appear in exactly one place — the glass card — plus the alpha-plum borders.
Menus are opaque white; the ring around them is a solid page-coloured moat, not a shadow blur.

**Motion.** One curve, `cubic-bezier(.4,0,.2,1)`. Three durations: .2s opacity, .22s colour/border/shadow,
.28s for the account panel's max-height. Disclosure chevrons rotate 180° in .25s. There are no
entrance animations, no bounces, no springs, no skeleton shimmer.

**Layout.** A fixed 238px sidebar that sticks full-height, and a fluid main column padded 22/26/40.
The KPI row is `1.35fr` for the score plus four equal tiles, minimum 840px, scrolling horizontally
below that. Table rows are 38px, header rows 32px. Nothing else is fixed or floating; menus are
absolutely positioned against their trigger.

**Imagery vibe.** None of the usual kind. The visual warmth comes entirely from the blush-over-plum
bloom — warm, low-contrast, no grain, no photography.

---

## Iconography

- **One set.** 22 glyphs, all 24×24 viewBox, 1.5px stroke, `currentColor`, rounded caps and joins.
  They match the Solar / Iconify "linear" family, but the files here are the product's own.
- **Files:** `assets/icons/*.svg`.
- **Sizes:** 13px in table headers, 14px in buttons and menu rows, 15px in the sidebar and notices,
  16–20px only in specimen cards.
- **Colour:** `--ink-400` at rest, `--ink-500` in menus, `--plum-700` when a row is active, `--blush-400`
  on plum surfaces, `--rose-700` for destructive.
- **A glyph per tool** is a fixed mapping — git-branch = GitLab/Git hygiene, code-quality = Sonar,
  shield-security = Trivy, alert-triangle = Sentry/Errors, gauge-performance = K6/Performance,
  lock = ZAP. Reuse these; do not introduce vendor logos.
  (Note the repo's own fixed metric-kind order: SonarQube, Trivy, Sentry, GitLab — see `AGENTS.md`.)
- **No emoji, no unicode icons, no second icon library.** `▲ ▼ · —` are typography, not iconography.
- Boxed glyphs (26px tile, `--ink-100` fill, `--ink-200` border, 6px radius) are used only in two-line
  menu items.

---

## What is in this directory

| Path | What's in it |
| --- | --- |
| `styles.css` | Entry point — imports only. |
| `tokens/` | `fonts.css`, `colors.css`, `typography.css`, `spacing.css`, `radius.css`, `elevation.css`, `motion.css`, `base.css` |
| `assets/logo*.svg` | Primary mark (side-view layered petals), on-dark, and two mono cuts |
| `assets/icons/` | The 22 product glyphs |

### Not mirrored (read from the source project when needed)

- `components/` — React primitives with `.d.ts` and `.prompt.md` specs, grouped as
  **core** (`Button`, `Card`, `StatusChip` + `toneFor`, `MeterBar`, `Icon`, `LogoMark`),
  **forms** (`TextField`, `Select`, `SegmentedControl`, `FieldLabel`),
  **navigation** (`Sidebar` + `SidebarSection`, `NavItem`, `UserCard`, `Breadcrumb`),
  **data** (`ScoreCard`, `StatCard`, `MetricTable` + `MetricRow`, `Sparkline`, `Legend`),
  **feedback** (`Menu`, `MenuItem`, `MenuDivider`, `NoticeBar`).
  This app is Go templates, not React — the `.prompt.md` files are the useful part (anatomy and
  states) if a component needs rebuilding here.
- `ui_kits/lotus-dashboard/` — a click-through recreation of both screens.
- `guidelines/` — 20 specimen cards (colours, type, spacing, brand).
- `assets/logo-alt-topview*.svg` — an unresolved alternate mark. The dashboard uses the primary
  side-view mark, which is what is mirrored here.

There is no Toast, Avatar, Tabs, Modal, Tooltip or Pagination in this system, because Lotus does not
define one. Don't invent one without asking.
