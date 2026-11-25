# Brand assets

`packages/ui/lib/baishadow-brand.ts` defines the 百影 / BaiShadow lockup, three
blades rotated by 120 degrees with open channels between them, and the
teal-to-indigo gradient for light and dark surfaces. The rendered production assets are previewed in
`docs/assets/brand/preview.png`.

After changing the geometry, regenerate the assets and review the preview:

```sh
pnpm generate:brand
node scripts/brand/sync.mjs --check
node --test scripts/brand/sync.test.mjs
```

## Prerequisites

- Node 22.18 or later and `pnpm install`.
- Playwright Chromium (`pnpm exec playwright install chromium`), or an installed
  Chromium browser selected with `BRAND_CHROMIUM_EXECUTABLE`.
- Python 3 with Pillow for ICO, ICNS and GIF packaging. ICNS generation works
  on Linux and macOS; it does not require `iconutil`.
- Optional `oxipng` for lossless PNG optimization. Its absence does not skip assets.

For example, on Linux with Google Chrome installed:

```sh
BRAND_CHROMIUM_EXECUTABLE=/usr/bin/google-chrome pnpm generate:brand
```

Raster antialiasing can differ across browser versions and operating systems.
Use the same environment for byte comparisons. SVG synchronization is exact.

## Outputs

`sync.mjs` has no external dependencies and supports `--check` for CI. It writes:

- `apps/web/public/favicon.svg` and `apps/docs/app/icon.svg`: the gradient tile at 16 px.
- `apps/web/public/icons/icon.svg`: opaque full-bleed PWA source.
- `docs/assets/logo-{light,dark}.svg`: the complete Chinese and English lockup.
- `docs/assets/brand/mark.svg`: the standalone transparent mark.
- The generated block in `apps/mobile/components/brand/baishadow-logo.tsx`.
  Mobile keeps its own component and does not import `@baishadow/ui`.

The Go CLI login callback embeds the same wordmark in
`server/cmd/multica/cmd_auth.go`.

`generate.mjs` synchronizes those files, then renders:

| Output | Dimensions / use |
| --- | --- |
| `apps/desktop/build/icons/*.png` | 16–512 px Linux/Windows icons |
| `apps/desktop/build/icon.png` | 1024 px padded desktop tile |
| `apps/desktop/build/icon.ico` | 16–256 px Windows icon bundle |
| `apps/desktop/resources/icon.png`, `build/icon.icns` | macOS grid with soft shadow; Electron uses the PNG |
| `apps/web/public/icons/icon-{192,512}.png` | PWA icons |
| `apps/web/public/icons/apple-touch-icon.png`, `icon-maskable-512.png` | Opaque 180/512 px icons, masked by the OS |
| `apps/mobile/assets/icon.png` | Opaque 1024 px Expo icon |
| `apps/web/app/opengraph-image.png` | 1200×630 share card |
| `docs/assets/logo-{light,dark}.gif` | 720×256 lockup reveal |
| `docs/assets/brand/preview.png` | Production lockup, variants and small-size specimens |

At 24 px and below the symbol grows for legibility. The default tile uses a
midnight-blue gradient. The preview includes light, dark and monochrome specimens.

Web/desktop components consume the same geometry directly. The wordmark reveals
its symbol, Chinese name and English name in sequence; loading icons light up
three shadows in turn. Brand motion respects `prefers-reduced-motion`.

Existing screenshots in incident reports and product walkthroughs are historical
content, not generated logo assets. Third-party provider logos are independent.
Desktop bundles and installed applications need a rebuild/reinstall to pick up
updated source assets; this command does not replace installed applications.
