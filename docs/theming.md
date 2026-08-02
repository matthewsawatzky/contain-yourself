# Theming

The controller has one configurable colour: the accent. Everything else in the
palette is derived from it or fixed by the dark surface scale.

## Where the accent comes from

Resolution order, first match wins:

1. the workstation's own accent, when viewing or proxying that workstation
2. the signed-in user's accent, set at **Appearance**
3. the deployment default, `#ff6b00`

An unset value is stored as an empty string and means "inherit from the next
source". Saving `default` from either form clears the stored value.

Only six-digit hex is accepted. The value is interpolated into a stylesheet, so
the input alphabet is kept deliberately narrow; anything else is rejected
rather than coerced.

## Delivery

The palette is served from `GET /theme.css` as a `:root` custom-property block,
not as an inline `<style>` element. That keeps the strict `style-src 'self'`
Content-Security-Policy intact.

Two consequences worth knowing:

- **The link must come last in `<head>`.** `app.css` declares the same
  custom properties as static fallbacks at identical specificity, so a
  `/theme.css` link placed before it is silently overridden. A test asserts the
  ordering.
- **The response is `private, no-store`.** It varies per viewer, so a shared
  cache must never reuse it.

Pass `?workstation=<id>` to resolve a specific workstation's override. The
lookup is authorized exactly like the matching page, so the parameter cannot be
used to read a workstation the caller cannot already see.

## Contrast

`--on-accent` is the text colour painted on accent-filled surfaces such as
primary buttons. It is chosen by measuring WCAG relative luminance against both
near-black and white and taking whichever contrasts more, so a bright accent
gets dark text and a dark accent gets light text. Every bundled preset clears
3:1 against its chosen foreground, which is the AA threshold for the bold text
these surfaces use.

This is why the palette is resolved on the server: CSS cannot yet make that
decision portably. The picker mirrors the same rule in JavaScript for live
preview only; the server revalidates and recomputes on save.

## Theming an app UI

App traffic is proxied through the controller's own origin, so a page running
inside a workstation can read the palette with a relative request:

```js
const { theme } = await fetch("/api/v1/theme").then(r => r.json());
document.documentElement.style.setProperty("--brand", theme.accent);
document.documentElement.style.setProperty("--brand-text", theme.on_accent);
```

The response carries the resolved palette, the preset list, and the deployment
default. Field names are snake_case. `theme.source` reports which of the three
sources won, so a UI can choose to follow only an explicit workstation colour.

The endpoint resolves whatever credentials the request carries — a session, a
share cookie, or neither — and falls back to the default rather than failing,
so an app UI does not need to handle an unauthenticated case.

## Variables

| Variable | Meaning |
| --- | --- |
| `--accent` | the chosen colour |
| `--accent-strong` | lighter, for hover states |
| `--accent-muted` | accent blended toward the panel, for tinted fills |
| `--accent-soft` | accent blended toward the background, for large washes |
| `--on-accent` | contrast-checked text on accent surfaces |
| `--bg`, `--panel`, `--panel-raised`, `--line` | dark surface scale |
| `--text`, `--muted` | foreground scale |
| `--green`, `--warning`, `--danger` | state colours, independent of the accent |

State colours stay fixed: a workstation in an error state should read as an
error whatever the accent is.
