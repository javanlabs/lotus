sparklines.js — vendored from https://github.com/mitjafelicijan/sparklines
(BSD 2-clause), unmodified.

Two things to know before touching it:

  - It scans the DOM once, on script execution, for [data-sparkline="true"]
    and never again. Lotus loads its sparklines lazily over htmx, so
    lotus.js initializes swapped-in ones itself.
  - `initSparkline` is a top-level const in a classic script, so it is
    reachable as a bare global from another classic script without the
    file being patched. Keep the <script> tag non-module or that breaks.
