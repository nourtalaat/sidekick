# Sidekick assets

Public brand assets for Sidekick. The design guide lives outside this repo for now. All SVGs are hand-built vectors; text in the wordmark and lockups is converted to outlines, so no font is needed.

| Path | What it is for |
|---|---|
| `sidekick-icon.png` | The original 1024 px icon. Every vector here is redrawn to match it. |
| `logo/sidekick-mark.svg` | The full-color mask on a transparent background. For light backgrounds. |
| `logo/sidekick-mark-mono.svg` | One-color mask in navy. Uses `currentColor`, so inline SVG takes the text color. |
| `logo/sidekick-mark-mono-white.svg` | One-color mask in white, for dark backgrounds and photos. |
| `logo/sidekick-app-icon.svg` | The mask on the brand gradient in a rounded square. Use wherever the product shows its icon. |
| `logo/sidekick-app-icon-square.svg` | The same with square corners, for platforms that round the corners themselves (iOS, Slack). Matches `sidekick-icon.png`. |
| `logo/sidekick-wordmark-light.svg` | "sidekick" in navy, for light backgrounds. |
| `logo/sidekick-wordmark-dark.svg` | "sidekick" in off-white, for dark backgrounds. |
| `logo/sidekick-lockup-light.svg` | Icon tile next to the wordmark, for light backgrounds. The default logo for headers and docs. |
| `logo/sidekick-lockup-dark.svg` | The same for dark backgrounds. |
| `favicon/favicon.svg` | Browser tab icon. A simplified drawing with a bigger mask, so it reads at 16 px. |
| `favicon/favicon-16.png`, `favicon/favicon-32.png` | PNG fallbacks for the favicon. |
| `favicon/apple-touch-icon.png` | 180 px home screen icon for iOS. Square corners; iOS rounds them. |
| `favicon/icon-512.png` | 512 px icon for web app manifests. |
| `slack/sidekick-slack-avatar.png` | 512 px app avatar to upload in the Slack app settings. |
| `slack/sidekick-eyes.gif` | Optional animated custom emoji of the investigating eyes, 128 px, about 73 KB. Upload as `:sidekick-eyes:`; Sidekick can react with it while it investigates. |
| `slack/sidekick-eyes.png` | Still version of the same emoji. |
| `social/sidekick-social-preview.png` | 1280 by 640 GitHub social preview. Upload it in the repository settings. |
| `social/sidekick-social-preview.svg` | Source for the social preview. |
| `states/investigating.svg` | Animated: the eyes glance left and right while an investigation runs. Stops when the reader prefers reduced motion. |
| `states/investigating-static.svg` | Still version of the same state, for places that cannot animate. |
| `states/empty.svg` | Eyes looking up. For empty lists and first runs. |
| `states/error.svg` | Lids lowered, eyes looking down. For failed runs and broken sources. |
| `states/done.svg` | Eyes closed in a smile. For finished setup or an empty queue. |
| `tokens/tokens.json` | Design tokens in the W3C Design Tokens format: colors, type, spacing, radii, shadows, motion. |
| `tokens/tokens.css` | The same tokens as CSS custom properties. Light is the default; dark follows the system setting, and `data-theme="dark"` or `data-theme="light"` on any element forces a theme. |

Favicon markup:

```html
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="icon" href="/favicon-32.png" sizes="32x32" type="image/png">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
```

The fonts in the design (Inter, Nunito, JetBrains Mono) are under the SIL Open Font License 1.1 and are not bundled here. The icons in the design come from Lucide (ISC license).
