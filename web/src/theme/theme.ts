export const THEMES = ["forest", "tide", "wine", "forest-light"] as const;
export type ThemeName = (typeof THEMES)[number];

export const THEME_LABELS: Record<ThemeName, string> = {
  forest: "Forest (dark)",
  tide: "Tide (dark)",
  wine: "Wine (dark)",
  "forest-light": "Forest light",
};

const KEY = "sockswebui.theme";

export function loadTheme(): ThemeName {
  try {
    const v = localStorage.getItem(KEY);
    if (v && (THEMES as readonly string[]).includes(v)) return v as ThemeName;
  } catch {
    /* storage blocked: fall through to the default */
  }
  return "forest";
}

export function applyTheme(name: ThemeName): void {
  document.documentElement.dataset.theme = name;
  try {
    localStorage.setItem(KEY, name);
  } catch {
    /* non-fatal: the choice just will not persist */
  }
}
