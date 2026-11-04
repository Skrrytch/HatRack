# MVP 4: Details in the panel, colours and wallpapers

MVP 4 makes details quicker to pick and new hats quicker to dress. It builds
on [MVP 2](mvp-2.md) (panel, Setup, details) and is independent of
[MVP 3](mvp-3.md). Three slices, each usable on its own:

1. **Suggestions from eight weeks (done).** Detail suggestions reach back eight
   weeks instead of 14 days.
2. **A detail field on every hat card (done).** The panel gets a quiet input line at
   the bottom of each card; it is where the suggestions live.
3. **Six colours with matching wallpapers (done).** Setup offers six named colours
   for hats and, for each, two built-in wallpapers.

## 1. Suggestions from eight weeks

- One window for all suggestions: `core.RecentWindow` (56 days), used by the
  server for agents and panel and by the web UI's Now page.
- The server sends suggestions per hat instead of per hat and activity:
  `recent: {hat: [{detail, activity, last}]}`, newest first, each detail once
  per hat (ignoring case), at most 50 per hat. The activity is the one the
  detail was used with last; the panel uses it to sort and to show where a
  detail belongs.
- The old per-activity list (`recent_details`) is dropped; an older agent
  simply shows no suggestions.

## 2. A detail field on every hat card

The worn hat's card used to show the current detail and up to four recent
ones as chips. With eight weeks of details that would burst the card.

- **Look.** At the bottom of each card a slim line, barely set off from the
  card: a small `#`, the current detail or a dimmed "Detail …", and a fine
  line in the hat's colour that gets stronger when the field has the focus.
- **Chips.** The worn hat keeps two chips above the field: the two most
  recent details of the worn activity other than the current one. One click
  each, as before.
- **Suggestions.** A click into the field (or Tab, which jumps to the field
  of the focused card, at first the worn hat's) opens a list below it, drawn over the rest of the panel: at
  most eight lines, the detail on the left, its activity dimmed on the right.
  For the worn hat, details of the worn activity come first. Typing filters
  (prefix first, then substring, ignoring case). If the list does not fit, the
  list opens above the field, and only if it fits nowhere the window grows.
- **Keys.** Up/Down pick a suggestion, Enter takes the picked one or the typed
  text, Escape leaves the field (a second Escape closes the panel), Backspace
  edits. A click outside the field leaves it.
- **Effect.** On the worn hat: `detail` (the activity stays). On any other
  hat: `on <hat>` with the detail; the hat's `resume_activity` decides the
  activity, as for a click on the hat. A suggestion only sets the detail; the
  activity chips choose activities.
- The typed shortcut "rev PR 482" without the field keeps working.

## 3. Six colours with matching wallpapers

- **Palette.** Six named colours, chosen to be told apart easily (distance in
  CIELAB at least 33 for every pair): purple `#8c4ea3`, orange `#ee6d24`, aqua
  `#2f999c`, brown `#8a4a26`, pink `#c24e63`, mint `#bde292`. Teal was dropped
  (too close to aqua), brown darkened (too close to orange). They replace the
  fallback palette for new hats.
- **Setup.** The hat page and the new-hat form show the six colours as round
  swatches next to the free colour picker ("own colour"). Picking a swatch
  moves the two matching wallpapers to the front of the wallpaper gallery and
  marks them; if the hat has no wallpaper of its own yet, the first one is
  filled in.
- **Built-in wallpapers.** Twelve images (two sets × six colours), abstract
  gradients generated for HatRack, are embedded in the binary
  (`internal/assets/wallpapers`, scaled to 2560 px width). They are written
  to `~/.local/share/hatrack/wallpapers/` when a device lists its wallpapers
  or applies one, and only if missing or different.
- **Device-independent values.** The configuration stores
  `hatrack:wp01-purple` instead of a path. The wallpaper backends resolve it
  on each device, so one configuration fits machines with different home
  folders. The device's gallery lists the built-in images first, with these
  values.

## Not in MVP 4

- A matching desktop theme per colour (purple → `Mint-Y-Dark-Purple`).
- Wallpapers for colours outside the palette.
