import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const maxBytes = 1 << 20;
const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const examplesDir = path.resolve(process.argv[2] || path.join(scriptDir, "..", "examples"));
const files = fs.readdirSync(examplesDir)
  .filter((name) => name.endsWith(".html"))
  .sort();

let checkedScripts = 0;
let largest = { file: "", bytes: 0 };
for (const name of files) {
  const file = path.join(examplesDir, name);
  const bytes = fs.readFileSync(file);
  if (bytes.length > maxBytes) {
    throw new Error(`${name} exceeds the 1 MiB HTML limit: ${bytes.length} bytes`);
  }
  if (bytes.length > largest.bytes) {
    largest = { file: name, bytes: bytes.length };
  }
  const html = bytes.toString("utf8");
  if (name === "ck3-gui-conditional-localization-example.html") {
    if (!html.includes('data-ck3-initial-sim-visible="true"')) {
      throw new Error(`${name} must open with the real inventory button visible`);
    }
    if (!html.includes('data-ck3-texture="gfx/interface/icons/flat_icons/inventory.dds"') ||
        !html.includes('data-ck3-texture-resolved="true"') ||
        !html.includes("ck3-has-texture")) {
      throw new Error(`${name} must embed the resolved inventory texture`);
    }
    if (/id="ck3-node-0" class="[^"]*\bis-sim-hidden\b/.test(html)) {
      throw new Error(`${name} root inventory button must not start hidden`);
    }
  }
  const scripts = [...html.matchAll(/<script([^>]*)>([\s\S]*?)<\/script>/g)]
    .filter((match) => !/application\/json/i.test(match[1]));
  for (const match of scripts) {
    const source = match[2];
    // Parse the generated script without executing it.
    new Function(source);
    const hash = crypto.createHash("sha256").update(source).digest("base64");
    if (!html.includes(`script-src 'sha256-${hash}'`)) {
      throw new Error(`${name} does not pin its executable script with the exact CSP hash`);
    }
    checkedScripts += 1;
  }
}

console.log(JSON.stringify({
  files: files.length,
  checked_scripts: checkedScripts,
  largest,
}, null, 2));
