# Working conventions

Notes that outlive any single change. Read before touching the areas they
name; add to them when a decision here is settled rather than re-litigating
it in the next session.

## Metric kind order is fixed: SonarQube, Trivy, Sentry, GitLab

Wherever the four integrations appear as a list — table columns, KPI cards,
the bind-metric wizard, anything added later — they appear in that order,
every time. It is not alphabetical and not the order they were built in; it
is the order the team reads them in, and consistency is the whole point: a
reader who learns the third column is Sentry on one screen must not have to
re-learn it on the next.

One constant is the source of truth:

    internal/web/bindview.go → bindableKinds

Everything that renders kinds in sequence derives from it (or, where a list
carries extra per-column data, is asserted against it in a test). Adding a
fifth integration means adding it there, in the position the team decides,
and nowhere else.

The user-facing labels live beside it in `kindLabels`.

## Alpine `:style` must use the object form, never a string

`:style="'width:280px'"` REPLACES the whole `style` attribute; the static
styles written beside it silently vanish. `:style="{ width: '280px' }"`
merges instead.

This has bitten three separate times in this codebase, each time as a
puzzling visual bug rather than an error: the sidebar user menu lost its
`transition` and snapped open, the active segmented-control pill lost its
`padding` and `border-radius`, and an info popover lost its `width` and
`z-index`. Nothing throws — the declaration is simply gone.

Rule: anything with static styles in `style` and conditional styles in
`:style` uses the object form. If a string is genuinely easier to read,
move the static declarations into a CSS class instead.

## Commit straight to main, atomic and conventional

Work lands on `main` directly. Do not create a feature branch unless one
is asked for.

Each commit is atomic: one logical change, with the tests and docs that
belong to it, and the tree builds and passes `make lint` and `make test`
at every commit. Unrelated changes go in separate commits, even when
they were made together.

Messages follow Conventional Commits: `type(scope): summary`, where type
is one of `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `ci` or
`perf`, and scope is the area touched (`qa`, `projects`, `web`,
`metrics`, …). The body explains why, not what the diff already shows.

## A score's colour comes from the number, nothing else

Every score — a kind on a component, a component, a project — is 0–100
and has exactly one reading, `internal/scoring/bands.go`: 80–100 is
Healthy (green), 0–79 is Needs attention (red), no score is a grey "–"
with a reason word. A kind's status is `collector.StatusForScore` of its
score; a hard failure (failed quality gate, critical CVE, failed
pipeline) is a *cap* on the score, never a separate verdict, so a number
and its colour cannot disagree (`internal/collector/invariant_test.go`).
At the project level, if any component has failed (< 80), the project's
Lotus score is capped at the lowest component score, so a project cannot
read as healthy while one of its components needs attention.

Each level rounds once, half up (`scoring.RoundHalfUp`), at its end;
never floor or truncate, never round an intermediate. A missing score
is left out of every average, never zero. No letter grades outside the
SonarQube pages, where SonarQube's own A–E ratings are shown as its
inputs. After changing a formula, `lotus reproject` rewrites stored
headlines from snapshots without vendor calls.
