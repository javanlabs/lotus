---
name: lotus-design
description: Use this skill to generate well-branded interfaces and assets for Lotus, either for production or throwaway prototypes/mocks/etc. Contains essential design guidelines, colors, type, fonts, assets, and UI kit components for prototyping.
user-invocable: true
---

Read the README.md file within this skill, and explore the other available files.
If creating visual artifacts (slides, mocks, throwaway prototypes, etc), copy assets out and create
static HTML files for the user to view. If working on production code, you can copy assets and read
the rules here to become an expert in designing with this brand.
If the user invokes this skill without any other guidance, ask them what they want to build or
design, ask some questions, and act as an expert designer who outputs HTML artifacts _or_
production code, depending on the need.

**In this repository specifically:** the production UI is Go `html/template` + Alpine.js + Tailwind
(`web/templates/`, `web/static/css/app.css`, built to `app.built.css` by `make css`). It is not
React, so the upstream project's `.jsx` primitives are a spec to translate from, not code to import.
The token files here are plain CSS custom properties and drop straight in.
