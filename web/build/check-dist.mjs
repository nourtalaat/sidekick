// Fails the build when index.html could not run under the Content Security
// Policy that the Go server sends: no inline script, inline style, style
// attribute or inline event handler.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const indexPath = fileURLToPath(new URL("../../internal/web/dist/index.html", import.meta.url));
const html = readFileSync(indexPath, "utf8");

const problems = [];

for (const match of html.matchAll(/<script\b([^>]*)>/gi)) {
  if (!/\bsrc\s*=/.test(match[1])) {
    problems.push("an inline <script>");
  }
}

if (/<style\b/i.test(html)) {
  problems.push("a <style> block");
}

if (/\sstyle\s*=/i.test(html)) {
  problems.push("a style attribute");
}

if (/\son[a-z]+\s*=/i.test(html)) {
  problems.push("an inline event handler");
}

if (!html.includes('<div id="root"></div>')) {
  problems.push('no <div id="root"></div>');
}

if (problems.length > 0) {
  console.error(`index.html breaks the Content Security Policy: ${[...new Set(problems)].join(", ")}`);
  process.exit(1);
}
