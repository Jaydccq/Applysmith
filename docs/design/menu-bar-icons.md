# Menu bar companion icons

## Goal and acceptance

Replace the two macOS menu bar marks, PersonalCU and Applysmith, with legible
cartoon cat marks that belong to the existing career companion identity.
Applysmith carries an envelope; PersonalCU carries a pointer. These are template
icons so macOS renders them correctly on light and dark menu bars. PersonalCU
keeps distinct ready, paused and warning states, and both menus keep their
existing actions and status text.

Use editable SVG sources and small packaged PNGs. Verify the render at actual
menu bar size, build the two apps, verify their signatures, and reopen their
menus after installation. Keep PersonalCU's existing signing identity so its
permission grants continue to match. Work in an isolated build snapshot when
other changes are present; do not include unrelated uncommitted source edits.

## Artwork and build

- Applysmith: `app/icons/menu-bar.svg`, rasterized as `menu-bar.png` and
  embedded by `app/tray_icon.go`.
- PersonalCU: `computeruse/icons/menu-bar.svg`, rasterized to
  `computeruse/Sources/PersonalCU/Resources/menu-bar.png`. `MenuBarIcon.swift`
  draws its ready mark and adds pause or warning badges when needed.
- Regenerate both 44-pixel Retina PNGs with `swift app/icons/render-menu.swift`,
  then build with `make app` and `make computer-use`.
- The images are editable vector marks drawn for an 18–22 point menu bar,
  rather than resized detailed application-icon illustrations. macOS applies
  the menu bar tint to their alpha masks.

## Verification

- Existing Applysmith desktop-app tests passed.
- PersonalCU release build and all 20 existing tests passed.
- Inspected both marks at menu bar size in light and dark appearances, plus
  PersonalCU's pause and warning badges.
- Both installed bundles pass strict signature verification. PersonalCU's
  designated signing requirement matches its previous version; after restart
  it reports Ready with Accessibility, Screen Recording and Input Monitoring
  granted, and its previous control settings retained.
- Opened both installed menu bar items after restart. Applysmith lists its
  running status, needs-you count and dashboard/browser actions; PersonalCU
  lists Ready, its connection count and the existing pause/background controls.
- Repository hygiene checks passed. Previous bundles were retained in a
  temporary rollback directory during installation.
- PersonalCU's native menu opens on its status item; macOS reports
  `cannotComplete` when AXPress opens the menu, so inspect the resulting menu
  before deciding whether a click failed. Never repeat that delivered click
  just because its AX call returned that error.
