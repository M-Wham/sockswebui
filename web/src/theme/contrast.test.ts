import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { THEMES } from "./theme";

const css = readFileSync(new URL("./forest.css", import.meta.url), "utf8");

const TOKENS = [
  "--bg", "--surface", "--fg", "--muted", "--border", "--accent", "--accent-fg",
  "--danger-bg", "--danger-fg", "--ok-bg", "--ok-fg", "--error", "--focus",
  "--glow-a", "--glow-b", "--tag-up",
];

function parseDecls(body: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

const blocks: Record<string, Record<string, string>> = {};
for (const m of css.matchAll(/(:root[^{}]*)\{([^{}]*)\}/g)) {
  const sel = m[1];
  const named = sel.match(/data-theme="([\w-]+)"/);
  if (!named) continue;
  blocks[named[1]] = parseDecls(m[2]);
}
const base = blocks["forest"] ?? {};

function palette(name: string): Record<string, string> {
  return name === "forest" ? base : { ...base, ...blocks[name] };
}

function lum(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
    .map((v) => {
      const c = v / 255;
      return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    })
    .reduce((s, c, i) => s + c * [0.2126, 0.7152, 0.0722][i], 0);
}
function ratio(a: string, b: string): number {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

const TEXT: [string, string][] = [
  ["--fg", "--bg"], ["--fg", "--surface"], ["--muted", "--bg"], ["--muted", "--surface"],
  ["--muted", "--glow-a"], ["--accent-fg", "--accent"], ["--accent", "--bg"],
  ["--accent", "--surface"], ["--danger-fg", "--danger-bg"], ["--ok-fg", "--ok-bg"],
  ["--error", "--bg"], ["--error", "--surface"], ["--tag-up", "--bg"],
  ["--tag-up", "--surface"],
];
const UI: [string, string][] = [
  ["--border", "--bg"], ["--border", "--surface"], ["--focus", "--bg"], ["--focus", "--surface"],
];

describe("theme palettes", () => {
  it("palette blocks equal THEMES", () => {
    expect(Object.keys(blocks).sort()).toEqual([...THEMES].sort());
  });
  for (const name of THEMES) {
    describe(name, () => {
      const p = palette(name);
      it("defines every token", () => {
        for (const t of TOKENS) expect(p[t], `${name} ${t}`).toMatch(/^#[0-9a-fA-F]{6}$/);
      });
      for (const [min, pairs] of [[4.5, TEXT], [3, UI]] as const) {
        for (const [f, b] of pairs) {
          it(`${f} on ${b} >= ${min}`, () => {
            if (!p[f] || !p[b]) return expect.fail(`${name}: ${f}/${b} undefined`);
            const r = ratio(p[f], p[b]);
            expect(r, `${name} ${f}/${b} = ${r.toFixed(2)}`).toBeGreaterThanOrEqual(min);
          });
        }
      }
    });
  }
});
