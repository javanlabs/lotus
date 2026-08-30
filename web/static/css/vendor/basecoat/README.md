Vendored from basecoat-css v1.0.2 (https://basecoatui.com), MIT.

Only the three component stylesheets Lotus uses are here — Basecoat's CDN
bundles each ship their own Tailwind preflight, which would apply the reset
a second time on top of app.built.css. These are the Tailwind *sources*, so
`make css` compiles them through our single build instead.

They carry structure only: placement, transitions, the toast's collapse
animation. No colour, border, radius or shadow — that skin lives in
Basecoat's style packs, which we deliberately do not import, and Lotus
paints its own in app.css.

To upgrade: npm pack basecoat-css@<version>, copy dist/components/{combobox,
popover,toast}.css here and dist/js/{basecoat,popover,combobox,toast}.min.js
to web/static/vendor/basecoat-*.min.js, then re-check the token bridge in
app.css against any newly used theme utilities.
