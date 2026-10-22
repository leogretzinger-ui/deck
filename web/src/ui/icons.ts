// Lucide icons, rendered from the lucide-static package: icons-data.ts is
// generated from node_modules by tools/generate-icons.mjs, and
// icons.test.ts fails on any drift, so no path data is ever written by
// hand. Inline SVG rather than files under public/: the workspace builds
// its chrome by string concatenation, and an <img> cannot inherit
// currentColor, which every one of these relies on.
//
// Icons are decorative here — every control that carries one also carries
// its own accessible name — so each is marked aria-hidden. The 1.75
// stroke is the design's icon weight; lucide ships 2.
import { ICON_PATHS, type IconName } from "./icons-data.ts";

export type { IconName };

export function lucideIcon(name: IconName, size: number): string {
  return `<svg aria-hidden="true" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" class="flex-none">${ICON_PATHS[name]}</svg>`;
}

// githubMark is GitHub's own mark (Octicons mark-github, 16px grid). It is
// their trademark and is reproduced exactly; it takes the control's text
// colour like every icon here.
export function githubMark(size: number): string {
  return `<svg aria-hidden="true" width="${size}" height="${size}" viewBox="0 0 16 16" fill="currentColor" class="flex-none"><path d="M6.766 11.328c-2.063-.25-3.516-1.734-3.516-3.656 0-.781.281-1.625.75-2.188-.203-.515-.172-1.609.063-2.062.625-.078 1.468.25 1.968.703.594-.187 1.219-.281 1.985-.281.765 0 1.39.094 1.953.265.484-.437 1.344-.765 1.969-.687.218.422.25 1.515.046 2.047.5.593.766 1.39.766 2.203 0 1.922-1.453 3.375-3.547 3.64.531.344.89 1.094.89 1.954v1.625c0 .468.391.734.86.547C13.781 14.359 16 11.53 16 8.03 16 3.61 12.406 0 7.984 0 3.563 0 0 3.61 0 8.031a7.88 7.88 0 0 0 5.172 7.422c.422.156.828-.125.828-.547v-1.25c-.219.094-.5.156-.75.156-1.031 0-1.64-.562-2.078-1.609-.172-.422-.36-.672-.719-.719-.187-.015-.25-.093-.25-.187 0-.188.313-.328.625-.328.453 0 .844.281 1.25.86.313.452.64.655 1.031.655s.641-.14 1-.5c.266-.265.47-.5.657-.656"/></svg>`;
}
