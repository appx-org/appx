# Appx Style Guide

Darksynth cyberpunk aesthetic. Deep teal-black backgrounds, electric cyan accents, JetBrains Mono for data/labels, DM Sans for prose. High contrast, minimal chrome, no decorative elements. Extremely functional yet aesthetically pleasing.

---

## Color Palette

Defined as CSS variables in `web/src/index.css`. Always use these — never hardcode hex values in components.

### Backgrounds

| Variable          | Value     | Use                                |
| ----------------- | --------- | ---------------------------------- |
| `--bg`            | `#060c0e` | Page background, input backgrounds |
| `--surface`       | `#0d1214` | Cards, modals, panels              |
| `--surface-hover` | `#111a1e` | Hovered cards, skeleton shimmer    |

### Text

| Variable   | Value     | Use                                                         |
| ---------- | --------- | ----------------------------------------------------------- |
| `--text`   | `#e2f4f8` | Primary text — names, headings, input values                |
| `--muted`  | `#7ab8c8` | Secondary text — labels, nav, descriptions, timestamps      |
| `--subtle` | `#1a2c30` | Decorative/structural only — dividers, disabled backgrounds |

### Borders

| Variable   | Value                  | Use                                          |
| ---------- | ---------------------- | -------------------------------------------- |
| `--border` | `rgba(0,229,255,0.15)` | All borders — cards, inputs, modals, headers |

### Accent

| Variable     | Value                  | Use                                                      |
| ------------ | ---------------------- | -------------------------------------------------------- |
| `--cyan`     | `#00e5ff`              | Bright highlights only — hover states, active indicators |
| `--cyan-dim` | `rgba(0,229,255,0.10)` | Subtle cyan fill — hover backgrounds                     |
| `--blue`     | `#0369a1`              | Primary action buttons (solid fill with white text)      |

### Semantic

| Variable       | Value                    | Use                                                  |
| -------------- | ------------------------ | ---------------------------------------------------- |
| `--green`      | `#3ddc84`                | Running status, success, "configured"                |
| `--green-dim`  | `rgba(61,220,132,0.10)`  | Green button hover background                        |
| `--red`        | `#ff6b6b`                | Error status, destructive actions, validation errors |
| `--red-dim`    | `rgba(255,107,107,0.10)` | Error backgrounds, red button hover                  |
| `--yellow`     | `#f5c518`                | Transitional states (starting, stopping)             |
| `--yellow-dim` | `rgba(245,197,24,0.10)`  | Yellow hover backgrounds                             |

---

## Typography

Two fonts — one for UI prose, one for data/code/labels.

### DM Sans — body font

Used for: page body copy, button labels, headings, modal titles, nav items.

```
fontFamily: "'DM Sans', sans-serif"
```

| Scale         | fontSize | fontWeight | Use                           |
| ------------- | -------- | ---------- | ----------------------------- |
| Page heading  | 20px     | 500        | Wordmark                      |
| Section title | 15px     | 500        | Modal titles                  |
| Body          | 13–14px  | 400        | Descriptions, card names      |
| Small         | 12–13px  | 400        | Button labels, secondary copy |

### JetBrains Mono — data/code font

Used for: status badges, port numbers, form field labels (ALL CAPS), error messages, inline code, timestamps, transitional labels.

```
fontFamily: "'JetBrains Mono', monospace"
```

| Scale  | fontSize | letterSpacing | Use                                    |
| ------ | -------- | ------------- | -------------------------------------- |
| Label  | 10–11px  | `0.10–0.12em` | ALL-CAPS field labels, section headers |
| Status | 10px     | `0.07em`      | Status text (RUNNING, STOPPED, etc.)   |
| Meta   | 11px     | default       | Port numbers, error lines, hints       |
| Body   | 11–13px  | default       | Error banners, confirm text            |

**Rule:** if the text is machine-generated data, a fixed identifier, a status, or a form label — use Mono. If it's prose a human wrote — use DM Sans.

---

## Buttons

Four button types. Applied via `data-btn` attribute (drives CSS hover transitions in `index.css`) plus an inline `style` object.

### Primary — solid fill, white text

For the single most important action in a form (Create, Save).

```tsx
<button data-btn="primary" style={styles.createBtn}>Create</button>

createBtn: {
  background: 'var(--blue)',
  border: 'none',
  color: '#fff',
  borderRadius: 4,
  padding: '7px 20px',
  fontSize: 13,
  fontWeight: 500,
}
```

### Outline — coloured border, transparent fill

For reversible container actions (Start = green, Stop = red). Border opacity ~35% of the full colour.

```tsx
<button data-btn="outline-green" style={styles.outlineGreenBtn}>Start</button>

outlineGreenBtn: {
  background: 'transparent',
  border: '1px solid rgba(61,220,132,0.35)',
  color: 'var(--green)',
  borderRadius: 4,
  padding: '4px 14px',
  fontSize: 12,
  fontWeight: 500,
}
```

### Text — no border, no background

For secondary/tertiary actions (Cancel, Reset, Delete initial state, nav links).

```tsx
<button data-btn="text" style={styles.textBtn}>Cancel</button>

textBtn: {
  background: 'transparent',
  border: 'none',
  color: 'var(--muted)',
  padding: '4px 8px',
  fontSize: 12,
}
```

Text-red variant: `data-btn="text-red"` — same style, `color: 'var(--muted)'` at rest, transitions to `var(--red)` on hover.

### Nav — header navigation links

Same as text button with slightly larger padding.

```tsx
<button data-btn="text-nav" style={styles.navBtn}>Settings</button>

navBtn: {
  background: 'transparent',
  border: 'none',
  color: 'var(--muted)',
  padding: '5px 10px',
  fontSize: 13,
}
```

---

## Form Inputs

```tsx
input: {
  background: 'var(--bg)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  padding: '8px 12px',
  fontSize: 13,
  color: 'var(--text)',
  outline: 'none',
}
```

Focus ring is handled globally in `index.css`: `border-color: rgba(255,255,255,0.2)`.

Field labels above inputs use JetBrains Mono, ALL CAPS, `fontSize: 10`, `letterSpacing: '0.1em'`, `color: 'var(--muted)'`.

---

## Cards

```tsx
card: {
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  padding: '16px 18px',
}
```

Project cards add a `borderLeft: '2px solid <statusColor>'` to communicate state at a glance. Cards animate in with `fadeSlideIn` (staggered by index × 50ms).

Hover state is CSS-driven via `[data-card="project"]:hover` in `index.css` — adds `translateY(-1px)` and a `box-shadow`.

---

## Status Indicators

A 7px dot + ALL-CAPS Mono label. Both take their colour from `statusColor(status)`:

| Status              | Colour          |
| ------------------- | --------------- |
| running             | `var(--green)`  |
| starting / stopping | `var(--yellow)` |
| error               | `var(--red)`    |
| stopped             | `var(--muted)`  |

Stopped is intentionally muted — it's the neutral resting state, not an alarm.

---

## Layout & Spacing

- **Page max-width**: 1080px (dashboard), 600px (settings/forms)
- **Page padding**: `28px 24px`
- **Header height**: `14px 24px` padding, `1px solid var(--border)` bottom
- **Card gap**: 12px grid gap
- **Border radius**: 4px for cards/inputs/buttons; 6px for modals only

Spacing is 4px-based. Prefer multiples of 4 or 8 for padding/gap/margin. Common values: 4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24, 28.

---

## Modals

Overlay: `rgba(0,0,0,0.75)` fill + `backdrop-filter: blur(4px)`. Click-outside to close.

Modal panel: `var(--surface)` background, `1px solid var(--border)`, `borderRadius: 6`, `width: 360px`. Slightly larger border-radius (6px vs 4px) distinguishes floating panels from inline cards.

Animation: `fadeIn` keyframe on `[data-overlay]`.

---

## Animations

All defined in `index.css`:

| Name          | Duration      | Use                             |
| ------------- | ------------- | ------------------------------- |
| `fadeSlideIn` | 0.3s ease     | Card mount (staggered by index) |
| `fadeIn`      | 0.15s ease    | Modal/overlay appear            |
| `shimmer`     | 1.4s infinite | Skeleton loading placeholders   |

Hover transitions on interactive elements: `0.12s ease` for `background`, `color`, `box-shadow`, `transform`. Keep transitions short — this is a tool UI, not a marketing page.

---

## Writing Style (UI Copy)

- Labels and section headers: ALL CAPS, Mono — `SETTINGS`, `NAME`, `PORT`, `STATUS`
- Status values: ALL CAPS, Mono — `RUNNING`, `STOPPED`, `ERROR`
- Button labels: Title Case, DM Sans — `Start`, `Delete`, `New Project`
- Confirmation prompts: sentence case, Mono — `Delete all data?`
- Error messages: sentence case, no period if short — `Failed to start`, `Project name already exists`
- Empty states: two lines — bold/large muted noun first, small hint second

---

## Things to Avoid

- Hardcoded hex colours in component files — use CSS variables
- Pure black (`#000`) or pure white (`#fff`) backgrounds — the palette uses tinted near-blacks
- Adding colour to the UI that isn't in the palette — if a new semantic colour is needed, add it to `index.css` first
- `border-radius` above 6px — the aesthetic is angular, not soft
- Drop shadows on cards at rest — only on hover, and only subtle (`box-shadow: 0 4px 20px rgba(0,0,0,0.6)`)
- Gradients, glows, or scan-line effects as decorative chrome — restraint is the cyberpunk move
