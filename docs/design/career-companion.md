# Career companion visual design

## Goal and acceptance

Replace the dashboard visual identity and app artwork with a cohesive, cute
cartoon career companion. Keep the existing routes, actions, API reads and live
status data. Acceptance: a real illustrated Home cover, matching navigation and
native app icons, consistent shared surfaces and controls, readable light and
dark modes, and a layout that works on a phone and beside the browser panel.
Run the dashboard build and existing UI checks, then inspect desktop, narrow,
and dark browser renders and exercise the cover links and shell controls.

## Direction and assumptions

The app remains Applysmith and its interface remains English. The design uses
warm cream paper, forest green actions, sage surfaces, restrained apricot
accents, rounded cards, and an original cream cat with a sage neckerchief.
Cartoon details decorate the shell and cover; state indicators keep their
existing semantic colors. No provider, account or application state is changed.

The simplest path is to update existing shared CSS tokens and shell styling,
add one Home cover component, and wire generated artwork into existing asset
and app packaging paths. Use installed fonts without a network font dependency.
Illustration composition and small-icon legibility require visual inspection.

## Artwork

Generated with the built-in image generation tool. The original prompts are
recorded below; the cover and icon share the same original character.
Web assets are encoded as WebP to keep the initial transfer small. The native
app icon is PNG; build tooling creates platform icon resources from that PNG.

- [Home cover](../../web/src/assets/career-cover.webp): 140,272 bytes.
- [Web avatar and favicon](../../web/src/assets/career-cat.webp): 9,420 bytes.
- [Native icon](../../app/icons/icon.png): 1024x1024 RGBA, transparent margins.

## Generation prompts

### Home cover

```text
Use case: illustration-story
Asset type: wide cover illustration for the existing Applysmith desktop job-search application, usable as a hero banner.
Primary request: An adorable original cartoon cat career companion, warm and optimistic, professionally art-directed editorial cartoon.
Scene/backdrop: A calm warm ivory paper background with a whimsical miniature garden-workspace, a sage green laptop, a small stack of job application envelopes, a tiny potted plant and an apricot work satchel.
Subject: One round cream-white cat with small dark cocoa dot eyes, tiny triangular ears, peach cheeks, wearing a sage neckerchief, happily organizing a letter at the laptop. Charming expressive simple face, rounded paws, no human anatomy.
Style/medium: Beautiful 2D storybook cartoon, clean hand-drawn dark brown contour lines, matte flat gouache-like colors, subtle paper grain, refined small editorial illustration, adorable but suitable for an adult productivity app. No 3D.
Composition/framing: Very wide landscape, approximately 2.5:1. The cat and detailed workspace occupy the right 50 percent; keep the left 45 percent quiet warm ivory negative space where live interface text will be overlaid. Gentle rolling sage ground and two little decorative star shapes tie the scene together. Give generous safe padding.
Lighting/mood: Cozy, encouraging, light, calm, fresh.
Color palette: Warm ivory #faf7ef, deep forest #244b3b, sage #bfd0ae, apricot #edb181, cocoa #433c32.
Constraints: NO text, NO lettering, NO words, NO watermarks, NO UI, NO frames. Deliver a single finished banner illustration, not a design board or mockup.
```

### Web avatar

```text
Use case: logo-brand
Asset type: square application brand avatar and favicon for Applysmith.
Primary request: Create a standalone adorable cartoon cat app icon based on the provided illustration. Use the reference image ONLY for the cat character identity, colors, hand-drawn style.
Subject: The same round cream-white cat, tiny triangular ears, cocoa dot eyes, peach cheeks, sage green neckerchief, smiling and hugging one small apricot application envelope with a flower-shaped seal. Head and paws, very readable silhouette.
Style/medium: Refined flat 2D storybook cartoon with warm dark cocoa outlines and subtle matte paper texture, just like the reference.
Composition/framing: Square 1:1 full bleed soft pale sage background, cat centered, head and paws occupy about 75 percent of the canvas, generous 12 percent safety margin. Simple, legible as a tiny icon.
Constraints: No text, letters, words, mockups, frames, UI, complex scenery, or watermark. No pre-rounded outer corners; the app will mask the image. Deliver just one finished square icon.
```

### Native app icon

```text
Use case: precise-object-edit
Asset type: production macOS app icon PNG for Applysmith.
Input image: reference square cat icon is the edit target.
Primary request: Preserve this exact finished cute cat icon, its character, face, envelope, green background, colors and composition. Fit it inside a macOS-style rounded square with smooth large-radius corners, centered on a transparent square canvas, occupying 82 percent of the canvas width and height. Leave 9 percent truly transparent margin outside on each of all four sides. Preserve the artwork within the rounded square. Keep the simple hand-painted 2D appearance, do not add 3D effects or a large drop shadow.
Constraints: Actual transparent alpha outside the rounded square, no white outer background, no checkerboard painted into the image, no text, no new elements. Output a single square application icon.
```

## Native icon sizing revision

The native macOS icon needs an edge-to-edge cartoon rather than a character
inside a small inset tile. The replacement keeps the same cat, palette and
envelope, enlarges the face to dominate the square, and reduces outer padding
to about one percent. This revision affects the native PNG and ICNS only.

Acceptance: inspect the generated icon at full and Dock sizes, preserve PNG
alpha, generate every required ICNS size, install the resource, and verify the
app signature and the installed resource. Reuse the installed app binary so
an artwork-only update does not incorporate unrelated source edits. Record
the final prompt and verification results here after generation.

The replacement was generated with the built-in image tool and saved as
`app/icons/icon.png` (1024x1024 RGBA). Inspected a 128-pixel preview: the
character fills the square. macOS `iconutil` compiled and decoded all ten icon
representations, covering 16 through 1024 pixels. Conversion required execution
outside the filesystem sandbox; inside it, `iconutil` returned Invalid Iconset
for the same valid files.

Copied the installed app into a temporary staging bundle, replaced only
`Contents/Resources/AppIcon.icns`, preserved signing metadata, and applied an
ad-hoc signature. Signature verification passed before and after replacement;
the installed icon hash matches the compiled resource. Refreshed Launch Services
registration and reopened the app. The previous bundle is retained in the
temporary staging directory for rollback. No app source was recompiled, and the
existing protected prompt edit was left untouched.

### Full-size native icon prompt

```text
Use case: precise-object-edit
Asset type: macOS application icon, square, for Applysmith.
Input image: existing cream cartoon cat with sage scarf and application envelope is the edit target.
Primary request: Make the cat MUCH BIGGER and make the cute cartoon fill the ENTIRE icon. Keep the same original cat, cream fur, cocoa outlines, peach cheeks, sage neckerchief, happy face and apricot envelope with paw-shaped flower seal.
Composition: EXTREME CLOSE-UP cute cat app icon. The cat's face dominates the upper 75 percent of the icon. Ears extend almost to the top and side edges, cheeks nearly touch the left and right edges. Paws and the envelope are large in the lower quarter and may be naturally cropped at the bottom. The sage paper background fills a nearly edge-to-edge rounded square occupying 98 percent of the canvas. Only a 1 percent transparent margin around the rounded square, absolutely NO large padding. This is the artwork itself, not a small icon floating inside another image.
Style: Preserve the warm flat 2D hand-drawn storybook cartoon and subtle matte paper texture of the reference.
Constraints: Render one square icon, no presentation board, no shadow outside the icon, no text or letters. Corners smoothly rounded, outer corners truly transparent alpha. No scattered paint flecks or stray pixels outside the rounded square. Maximize the character size and legibility.
```

## Verification of the original dashboard design

- Dashboard production build and TypeScript check passed.
- The full dashboard suite initially passed 651 of 652 tests; the remaining
  contrast assertion exposed a green status color at 4.48:1 on the new sidebar.
  Darkening that status color fixed it. The final focused run passed all 21
  existing Home, shell and stylesheet tests.
- Optional happy-dom axe runs passed Home but encountered asynchronous
  navigation wait timeouts in shell tests. No test deadlines or assertions were
  changed. Ordinary shell tests and real-browser navigation passed.
- Headless Chrome opened all 18 routes at 1440, 768 and 390 pixels, plus Home
  at 360, 560 and 1024 pixels: 57 layouts, no horizontal document overflow and
  no page exceptions. Inspected desktop, phone, dark and browser-panel renders.
  Exercised both cover links, the sidebar rail, theme cycling and browser panel.
  Vite's development console also warned about the existing conditional `use()`
  page loader during navigation. That loader was not changed by this design
  work; native production rendering was inspected separately.
- Repository hygiene checks and the packaged-bundle check passed.
- `make app` produced an ad-hoc signed 48.3 MB app. Signature verification
  passed before and after installation. Replaced the installed bundle and
  reopened it; native UI inspection confirmed the cat avatar, cover and live
  dashboard. The previous bundle is retained in a temporary rollback copy.
- The artwork and shared styling changed; no backend code or user data was
  edited. The protected agent prompt and disclosure files were untouched.
