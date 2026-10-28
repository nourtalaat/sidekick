// Prints the NOTICE entries of the JavaScript packages and font files in the
// web UI's bundle, in the format of scripts/notice.sh. `make notice` appends
// the output to NOTICE after a UI build, which writes the list of bundled
// packages that this reads.
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const RULE = "=".repeat(80);
const webDir = fileURLToPath(new URL("..", import.meta.url));
const bundledFile = `${webDir}.build/bundled-packages.json`;

if (!existsSync(bundledFile)) {
  console.error(`notice: ${bundledFile} is missing; run the UI build first`);
  process.exit(1);
}

const manifest = JSON.parse(readFileSync(`${webDir}package.json`, "utf8"));
const bundled = JSON.parse(readFileSync(bundledFile, "utf8"));

const fonts = Object.keys(manifest.dependencies)
  .filter((name) => name.startsWith("@fontsource/"))
  .map(readFontPackage);

const entries = [...bundled.map((pkg) => ({ ...pkg, label: pkg.name })), ...fonts].sort((a, b) =>
  a.label < b.label ? -1 : a.label > b.label ? 1 : 0,
);

console.log(`\n${RULE}\nJavaScript packages in the web UI\n${RULE}`);
console.log("\nThe web UI is built from the npm packages below, which are bundled into the");
console.log("binary, and the font files of the @fontsource packages.");

for (const entry of entries) {
  console.log(`\n${RULE}\nnpm: ${entry.label} ${entry.version}\n${RULE}`);

  for (const file of entry.files) {
    printFile(file);
  }
}

function readFontPackage(name) {
  const dir = `${webDir}node_modules/${name}`;
  const pkg = JSON.parse(readFileSync(`${dir}/package.json`, "utf8"));
  const files = readdirSync(dir)
    .filter((file) => /^(licen[cs]e|copying|notice)/i.test(file))
    .sort()
    .map((file) => ({ name: file, text: readFileSync(`${dir}/${file}`, "utf8") }));

  return { label: `${name} (font files)`, version: pkg.version, files };
}

function printFile(file) {
  if (/^notice/i.test(file.name)) {
    console.log(`\n${file.name}:\n`);
    process.stdout.write(file.text);

    return;
  }

  if (isApache(file.text) && !hasOtherLicense(file.text)) {
    console.log(`\n${file.name}: Apache License, Version 2.0 (text in LICENSE)`);

    for (const line of file.text.split("\n")) {
      if (/^\s*Copyright /.test(line) && !/\[yyyy\]|\{yyyy\}/.test(line)) {
        console.log(line.trim());
      }
    }

    return;
  }

  console.log(`\n${file.name}:\n`);
  process.stdout.write(file.text);
}

function isApache(text) {
  return /^\s*Apache License\s*$/m.test(text) && text.includes("Version 2.0, January 2004");
}

function hasOtherLicense(text) {
  return text.includes("Permission is hereby granted") || text.includes("Redistribution and use");
}
