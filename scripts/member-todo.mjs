#!/usr/bin/env node
// Repository entry for the shared member-todo generator and precheck.
//
// The implementation ships inside the rd-acceptance and rd-project-management
// skill bundles; this wrapper lets repository work use the same code. It never
// calls the platform itself and never creates a real todo.

import { realpathSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { runMemberTodoCli } from "../agent-team/skills/rd-acceptance/scripts/member-todo.mjs";

export function main(argv = process.argv.slice(2), stdout = process.stdout) {
  return runMemberTodoCli(argv, stdout);
}

function isEntryPoint() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return import.meta.url === pathToFileURL(process.argv[1]).href;
  }
}

if (isEntryPoint()) {
  try {
    process.exitCode = main();
  } catch (error) {
    process.stderr.write(`member-todo: ${error.message}\n`);
    process.exitCode = 2;
  }
}