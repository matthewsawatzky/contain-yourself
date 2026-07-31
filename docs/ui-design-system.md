# Controller UI design system

Contain Yourself uses a compact, flat control-plane interface inspired by
infrastructure dashboards: information density is preferred over decoration,
status is visible at a glance, and important actions remain close to the
resource they affect.

This guide is the shared reference for controller contributors and app
publishers who want their application UI to feel native when opened through a
workstation.

## Principles

1. Use flat surfaces and one-pixel borders; reserve shadows for authentication
   dialogs and overlays.
2. Keep one primary action per panel. Secondary and destructive actions should
   be visually quieter.
3. Show status with text as well as color.
4. Prefer short labels, visible units, and defaults that can be accepted
   without understanding Docker internals.
5. Preserve keyboard focus, readable contrast, and responsive layouts down to
   360 CSS pixels.

## Shared tokens

Apps can mirror these CSS custom properties:

```css
:root {
  --cy-bg: #0a0c10;
  --cy-panel: #11151b;
  --cy-panel-raised: #151a22;
  --cy-line: #262c36;
  --cy-line-strong: #394252;
  --cy-text: #f3f5f7;
  --cy-muted: #929baa;
  --cy-accent: #8b7cff;
  --cy-success: #42d3a2;
  --cy-warning: #f1c75b;
  --cy-danger: #ff6b78;
  --cy-radius: 7px;
}
```

Use the system sans-serif stack for controls and
`ui-monospace, SFMono-Regular, Menlo, monospace` for identifiers, addresses,
commands, and logs.

## App layout

An app should work at a proxied base path such as `/apps/browser/`; do not
assume it is hosted at `/`. Keep the top-level background `--cy-bg`, use
`--cy-panel` for toolbars and inspectors, and use the accent only for the main
action or active navigation item.

Recommended structure:

```html
<header class="app-bar">App name · connection/status · actions</header>
<main class="app-content">Primary application surface</main>
```

App icons should be square SVGs with a transparent background, readable at
32×32 pixels, and should not contain embedded remote resources. The catalogue
bundle remains the source of truth for the icon.

## Controls and states

- Inputs: 38–42 pixels tall, seven-pixel radius, visible focus ring.
- Panels: one-pixel `--cy-line` border and ten-pixel radius.
- Primary action: solid `--cy-accent`, dark text.
- Error: `--cy-danger` plus a readable message and recovery action.
- Loading: state text such as “Pulling image” or “Waiting for VPN”; never rely
  on an indefinite spinner alone.

The controller implementation lives in `web/static/app.css`. Changes to
tokens or interaction patterns should update this document in the same pull
request.
