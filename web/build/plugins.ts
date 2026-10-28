import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { Plugin } from "vite";

const KEEP_FILE = ".gitkeep";
const LICENSE_FILE = /^(licen[cs]e|copying|notice)/i;
const PACKAGE_IN_PATH = /node_modules\/((?:@[^/]+\/)?[^/]+)\//g;

export interface BundledPackage {
  name: string;
  version: string;
  license: string;
  files: { name: string; text: string }[];
}

/**
 * Empties the output directory before a build but keeps .gitkeep, which lets
 * the Go package embed the directory before any build has run.
 */
export function cleanOutDir(outDir: string): Plugin {
  return {
    name: "sidekick-clean-out-dir",
    apply: "build",
    buildStart() {
      if (!existsSync(outDir)) {
        return;
      }

      for (const entry of readdirSync(outDir)) {
        if (entry !== KEEP_FILE) {
          rmSync(join(outDir, entry), { recursive: true, force: true });
        }
      }
    },
  };
}

/**
 * Writes the npm packages that ended up in the bundle, with their license and
 * notice files, to a JSON file that `make notice` turns into NOTICE entries.
 */
export function bundledPackages(file: string): Plugin {
  return {
    name: "sidekick-bundled-packages",
    apply: "build",
    generateBundle(_options, bundle) {
      const roots = new Set<string>();

      for (const output of Object.values(bundle)) {
        if (output.type !== "chunk") {
          continue;
        }

        for (const [id, module] of Object.entries(output.modules)) {
          if (module.renderedLength > 0) {
            const root = packageRoot(id);
            if (root) {
              roots.add(root);
            }
          }
        }
      }

      const packages = [...roots].map(readPackage).sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));

      mkdirSync(dirname(file), { recursive: true });
      writeFileSync(file, `${JSON.stringify(packages, null, 2)}\n`);
    },
  };
}

function packageRoot(id: string): string | undefined {
  const path = id.split("?")[0] ?? id;
  let last: RegExpExecArray | null = null;

  for (const match of path.matchAll(PACKAGE_IN_PATH)) {
    last = match;
  }

  if (!last) {
    return undefined;
  }

  return path.slice(0, last.index + last[0].length);
}

function readPackage(root: string): BundledPackage {
  const manifest = JSON.parse(readFileSync(join(root, "package.json"), "utf8")) as {
    name: string;
    version: string;
    license?: string;
  };

  const files = readdirSync(root)
    .filter((name) => LICENSE_FILE.test(name))
    .sort()
    .map((name) => ({ name, text: readFileSync(join(root, name), "utf8") }));

  return { name: manifest.name, version: manifest.version, license: manifest.license ?? "UNKNOWN", files };
}
