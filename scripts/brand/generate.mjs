#!/usr/bin/env node
// Rebuilds every brand asset from packages/ui/lib/baishadow-brand.ts. Prerequisites
// and the list of outputs: scripts/brand/README.md.
//
//   node scripts/brand/generate.mjs
import { chromium } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { loadBrand, svgs, sync } from "./sync.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "../..");
const brand = await loadBrand(root);
const { MARK_PATH, BRAND_COLORS } = brand;
const INK = BRAND_COLORS.navy, PAPER = BRAND_COLORS.white;
const { tile, wordmark } = svgs(brand);

function run(cmd, args) {
  const r = spawnSync(cmd, args, { stdio: "inherit" });
  if (r.error?.code === "ENOENT") throw new Error(`${cmd} not found; see scripts/brand/README.md`);
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited ${r.status}`);
}

const out = mkdtempSync(join(tmpdir(), "baishadow-brand-"));
const repo = (rel) => join(root, rel);
const pngs = [];

const updated = await sync(root);
if (updated.length) console.log(`updated ${updated.join(", ")}`);

const inter = createRequire(repo("apps/desktop/package.json")).resolve("@fontsource-variable/inter/files/inter-latin-wght-normal.woff2");
const fonts = `@font-face{font-family:"Inter";font-weight:100 900;src:url(data:font/woff2;base64,${readFileSync(inter).toString("base64")}) format("woff2")}`;

// Set BRAND_CHROMIUM_EXECUTABLE to use a locally installed Chromium browser.
const browser = await chromium.launch(process.env.BRAND_CHROMIUM_EXECUTABLE
  ? { executablePath: process.env.BRAND_CHROMIUM_EXECUTABLE }
  : { channel: "chromium" });
try {
  const page = await browser.newPage({ deviceScaleFactor: 1 });
  async function png(file, [w, h], body, { alpha = true } = {}) {
    mkdirSync(dirname(file), { recursive: true });
    await page.setViewportSize({ width: w, height: h });
    await page.setContent(`<html><head><style>${fonts}*{margin:0}html,body{width:${w}px;height:${h}px;overflow:hidden;background:transparent}svg{display:block}</style></head><body>${body}</body></html>`);
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: file, omitBackground: alpha });
    if (file.startsWith(root)) pngs.push(file);
  }
  const at = (svg, px, off) => `<div style="position:absolute;left:${off}px;top:${off}px;width:${px}px;height:${px}px">${svg}</div>`;

  // Desktop and PWA tiles: [tile px, margin] per canvas size.
  const FLAT = { 16: [16, 0], 24: [24, 0], 32: [30, 1], 48: [46, 1], 64: [52, 6], 128: [104, 12], 256: [208, 24], 512: [412, 50] };
  for (const [s, [px, off]] of Object.entries(FLAT)) {
    await png(repo(`apps/desktop/build/icons/${s}x${s}.png`), [+s, +s], at(tile(px), px, off));
    if (+s <= 256) await png(join(out, `ico-${s}.png`), [+s, +s], at(tile(px), px, off));
  }
  await png(repo("apps/desktop/build/icon.png"), [1024, 1024], at(tile(824), 824, 100));
  await png(repo("apps/web/public/icons/icon-192.png"), [192, 192], at(tile(156), 156, 18));
  await png(repo("apps/web/public/icons/icon-512.png"), [512, 512], at(tile(412), 412, 50));

  // iOS, Android and Expo mask these themselves, so they are full-bleed and opaque.
  await png(repo("apps/web/public/icons/apple-touch-icon.png"), [180, 180], tile(180, { bleed: true }), { alpha: false });
  await png(repo("apps/web/public/icons/icon-maskable-512.png"), [512, 512], tile(512, { bleed: true }), { alpha: false });
  await png(repo("apps/mobile/assets/icon.png"), [1024, 1024], tile(1024, { bleed: true }), { alpha: false });

  // macOS: the tile on Apple's 1024 grid (body 100..924) with the standard soft shadow.
  const macOS = (S) => {
    const k = S / 1024, px = Math.round(824 * k), off = Math.round(100 * k);
    return `<div style="position:absolute;left:${off}px;top:${off}px;width:${px}px;height:${px}px;filter:drop-shadow(0 ${(22 * k).toFixed(2)}px ${(30 * k).toFixed(2)}px rgba(0,0,0,.32))">${tile(px)}</div>`;
  };
  await png(repo("apps/desktop/resources/icon.png"), [1024, 1024], macOS(1024));
  const iconset = join(out, "icon.iconset");
  for (const [name, S] of [["16x16", 16], ["16x16@2x", 32], ["32x32", 32], ["32x32@2x", 64], ["128x128", 128], ["128x128@2x", 256], ["256x256", 256], ["256x256@2x", 512], ["512x512", 512], ["512x512@2x", 1024]]) {
    await png(join(iconset, `icon_${name}.png`), [S, S], macOS(S));
  }

  await png(repo("apps/web/app/opengraph-image.png"), [1200, 630], `<div style="position:relative;width:1200px;height:630px;background:${INK};overflow:hidden;font-family:Inter">
  <svg viewBox="0 0 100 100" style="position:absolute;width:780px;height:780px;right:-250px;bottom:-90px"><path fill="#1d3856" d="${MARK_PATH}"/></svg>
  <div style="position:absolute;left:64px;top:98px;height:116px">${wordmark({ dark: true, extra: ' height="116"' })}</div>
  <div style="position:absolute;left:64px;bottom:76px;color:#fff;font:600 66px/1.12 Inter;letter-spacing:-.02em">People and agents,<br>working as one team.</div></div>`, { alpha: false });
  await png(repo("docs/assets/brand/preview.png"), [1200, 760], `<main style="box-sizing:border-box;height:760px;background:#f0f3f7;padding:36px;display:grid;grid-template-rows:424px 244px;gap:20px;font-family:Inter;color:${INK}">
    <div style="display:grid;place-items:center;background:white;border-radius:24px;border:1px solid #e2e8f2">
      ${wordmark({ extra: ' width="785" height="298"' })}
    </div>
    <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:20px">
      <section style="display:grid;place-content:center;justify-items:center;gap:18px;background:#101e36;border-radius:20px;color:white">
        ${wordmark({ dark: true, id: "reverse-wordmark", extra: ' width="268" height="102"' })}
        <span style="font-size:13px;font-weight:500">Dark background</span>
      </section>
      <section style="display:grid;place-content:center;justify-items:center;gap:14px;background:white;border:1px solid #e2e8f2;border-radius:20px">
        ${tile(132, { id: "preview-icon" })}
        <div style="display:flex;gap:12px;align-items:center">${[16,24,32].map(px => tile(px, { id: `preview-${px}` })).join("")}</div>
      </section>
      <section style="display:grid;place-content:center;justify-items:center;gap:18px;background:#dfe7ef;border-radius:20px">
        <svg width="112" height="112" viewBox="0 0 100 100"><path fill="${INK}" d="${MARK_PATH}"/></svg>
        <span style="font-size:13px;font-weight:500">Monochrome</span>
      </section>
    </div>
  </main>`, { alpha: false });
  await page.close();

  // The same three-part reveal used by BaiShadowWordmark.
  const W = 720, H = 256, FPS = 30, TOTAL_MS = 1600;
  const css = `@keyframes reveal { from { opacity:0; transform:translateY(5px); } }
.reveal { animation:reveal .35s ease both; }`;
  const writeOn = (dark) => wordmark({ dark, extra: ' width="600"' })
    .replace(/<path /g, (() => { let i = 0; return () => `<path class="reveal" style="animation-delay:${100 + i++ * 150}ms" `; })());
  for (const [name, bg, dark] of [["light", PAPER, false], ["dark", INK, true]]) {
    const dir = join(out, `gif-${name}`);
    mkdirSync(dir);
    const gif = await browser.newPage({ viewport: { width: W, height: H }, deviceScaleFactor: 1 });
    await gif.setContent(`<html><head><style>*{margin:0}html,body{width:${W}px;height:${H}px;background:${bg};overflow:hidden}body{display:grid;place-items:center}${css}</style></head><body>${writeOn(dark)}</body></html>`);
    await gif.evaluate(() => document.getAnimations().forEach((a) => a.pause()));
    for (let k = 0; k < Math.round((TOTAL_MS / 1000) * FPS); k++) {
      await gif.evaluate(async (t) => {
        document.getAnimations().forEach((a) => { a.currentTime = t; });
        for (let i = 0; i < 2; i++) await new Promise(requestAnimationFrame);
      }, (k * 1000) / FPS);
      await gif.screenshot({ path: join(dir, `f${String(k).padStart(4, "0")}.png`) });
    }
    await gif.close();
  }
} finally {
  await browser.close();
}

run("python3", [join(here, "pack.py"), out, root]);
const optimizer = spawnSync("oxipng", ["-o", "2", "--strip", "safe", "-q", ...pngs], { stdio: "inherit" });
if (optimizer.error?.code === "ENOENT") console.log("oxipng unavailable; using Chromium PNG encoding");
else if (optimizer.status !== 0) throw new Error("PNG optimization failed");
rmSync(out, { recursive: true, force: true });
console.log("done");
