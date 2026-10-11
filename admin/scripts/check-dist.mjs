// SPDX-License-Identifier: GPL-3.0-or-later

// Fails the build when dist/ has anything a strict CSP (no inline scripts, no eval) would
// block, or anything from mock mode.
import { readdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

const dist = new URL("../dist/", import.meta.url).pathname;
const problems = [];

async function* files(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) yield* files(path);
    else yield path;
  }
}

for await (const path of files(dist)) {
  const name = path.slice(dist.length);
  if (/mockServiceWorker/.test(name)) problems.push(`${name}: the mock service worker must not be built`);
  if (!/\.(html|js|css)$/.test(name)) continue;
  const text = await readFile(path, "utf8");
  if (name.endsWith(".html")) {
    for (const tag of text.match(/<script\b[^>]*>/g) ?? []) {
      if (!/\bsrc=/.test(tag)) problems.push(`${name}: inline <script>`);
    }
    if (/<style\b|\sstyle=/.test(text)) problems.push(`${name}: inline style`);
    if (/\son[a-z]+=/.test(text)) problems.push(`${name}: inline event handler`);
  }
  if (name.endsWith(".js")) {
    if (/\beval\(|new Function\(/.test(text)) problems.push(`${name}: eval or new Function`);
    if (/setupWorker|mockServiceWorker/.test(text)) problems.push(`${name}: contains mock mode code`);
  }
}

if (problems.length > 0) {
  console.error(`dist/ isn't safe to serve under a strict CSP:\n  ${problems.join("\n  ")}`);
  process.exit(1);
}
// Go embeds dist/, which therefore always holds this placeholder (it is committed, and
// recreated here because the build empties the directory).
await writeFile(join(dist, ".keep"), "");
console.log("dist/ checked: no inline scripts, eval or mock code.");
