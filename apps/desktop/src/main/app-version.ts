import { app } from "electron";
import { execFileSync } from "node:child_process";

/**
 * Resolve the running app version. In packaged builds this is the value
 * `electron-builder` baked into package.json via `extraMetadata.version`
 * (driven by `git describe` — see `apps/desktop/scripts/package.mjs`), so
 * `app.getVersion()` matches the GitHub Release tag exactly.
 *
 * In dev, use `git describe` so diagnostics identify the running checkout.
 * Fall back to the package.json version when Git is unavailable.
 */
export function getAppVersion(): string {
  if (app.isPackaged) {
    return app.getVersion();
  }
  try {
    // argv array, not a shell string: the `v[0-9]*` match pattern must reach
    // git as one literal argument. A shell string breaks on Windows, where
    // cmd.exe keeps the single quotes and git matches no tag.
    const raw = execFileSync(
      "git",
      ["describe", "--tags", "--match", "v[0-9]*", "--always", "--dirty"],
      {
        cwd: app.getAppPath(),
        encoding: "utf-8",
        stdio: ["ignore", "pipe", "ignore"],
      },
    ).trim();
    if (!raw) return app.getVersion();
    return raw.replace(/^v/, "");
  } catch {
    return app.getVersion();
  }
}
