# Themes

plugdash ships four built-in themes — **Dark** (default), **Light**, **Monokai**,
and **Matrix** (green CRT) — and lets you drop in your own as plain CSS files. Pick a
theme in **Settings → Themes**; the choice is saved per browser.

## How themes work

A theme is just a set of CSS custom properties (variables) under a
`[data-theme="<id>"]` selector. The app sets `data-theme` on `<html>` and every
widget, card, button, etc. reads its colors from those variables — so overriding
the variables re-skins the whole UI. The built-in palettes live in
`web/assets/style.css` (`:root` / `[data-theme="dark"]` is the default set).

## Adding your own theme (no rebuild)

1. **Find the themes directory.** It's set by `--themes-dir`
   (or `$PLUGDASH_THEMES_DIR`), defaulting to `~/.config/plugdash/themes`. In the
   Docker image it defaults to `/data/themes`. Create it if it doesn't exist.

2. **Drop a `.css` file in it.** The **file name is the theme id** and must be
   letters/digits/`-`/`_` (e.g. `ocean.css` → id `ocean`, shown as "Ocean"). The
   file must target `[data-theme="<id>"]` matching that name:

   ```css
   /* plugdash-theme: Ocean */
   [data-theme="ocean"] {
     --bg: #07131f;
     --bg-elev: #0d2236;
     --bg-elev-2: #122c44;
     --border: #1d3a52;
     --border-soft: #14283a;
     --border-strong: #2c5274;
     --text: #dcecff;
     --text-muted: #8fb0cf;
     --text-dim: #5f7d9a;
     --accent: #36c5f0;
     --accent-hover: #6dd6f6;
     --accent-soft: rgba(54, 197, 240, 0.14);
     --accent-glow: rgba(54, 197, 240, 0.4);
     --green: #2ee6a8;
     --red: #ff6b81;
     --yellow: #ffd166;
     --pill-bg: #122c44;
     --card-top: #0c2032;
     --topbar-bg: rgba(7, 19, 31, 0.8);
     --on-accent: #04121f;
     color-scheme: dark;
   }
   ```

   The optional `/* plugdash-theme: Name */` header sets the picker label; without
   it the label is the prettified id. A ready-to-copy example is in
   [`examples/themes/ocean.css`](../examples/themes/ocean.css).

3. **Reload the page.** The new theme appears in **Settings → Themes** with a live
   preview, and is selectable like the built-ins.

## The variables

Copy the full set from the default theme in `web/assets/style.css` (the
`:root, [data-theme="dark"]` block) and recolor. The most impactful ones:

| Variable | Used for |
| --- | --- |
| `--bg`, `--bg-elev`, `--bg-elev-2` | page / card / nested surfaces |
| `--border`, `--border-soft`, `--border-strong` | borders |
| `--text`, `--text-muted`, `--text-dim` | text, in three emphases |
| `--accent`, `--accent-hover`, `--accent-soft`, `--accent-glow` | primary accent (links, active states, charts) |
| `--green`, `--red`, `--yellow` | widget status colors (ok / fail / warn) |
| `--card-top` | top of the card gradient |
| `--on-accent` | text/icon on an accent-filled surface |
| `--topbar-bg` | sticky top bar (semi-transparent) |
| `color-scheme` | `dark` or `light` (native form controls / scrollbars) |
| `--font`, `--mono` | optional: override the font stacks |

Any variable you omit falls back to the default (dark) value.

## Beyond colors

Because the file is real CSS scoped to `[data-theme="<id>"]`, you can also add
rules — e.g. `[data-theme="ocean"] .card { border-radius: 4px; }` — to restyle
specific elements, not just recolor. Keep selectors prefixed with
`[data-theme="<id>"]` so they only apply when your theme is active.

## Notes

- Files are served (concatenated) at `/api/themes.css` and listed at
  `/api/themes`. There's no per-theme sandboxing — only drop files you trust, the
  same as external plugins.
- Editing or adding a file takes effect on the next page reload (no restart).
- Each file is capped at 512 KiB; non-`.css` files and names with unusual
  characters are ignored.
