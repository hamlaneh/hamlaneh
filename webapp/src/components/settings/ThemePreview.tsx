import type { ThemePreference } from "../../theme";

/**
 * A real preview built from the theme's own tokens, not a coloured square —
 * `settings-components` §02 says so, and the light and dark previews are drawn
 * as miniature chat shells. The `system` preview is the two halves side by
 * side, which is exactly what "follows your operating system" looks like.
 *
 * Purely decorative: the radio beside it carries the choice, so this is hidden
 * from assistive technology.
 */
export function ThemePreview({ preference }: { preference: ThemePreference }) {
  if (preference === "system") {
    return (
      <span className="hm-theme-preview hm-theme-preview--split" aria-hidden="true">
        <span className="hm-theme-preview__half" data-scheme="light">
          <span className="hm-theme-preview__line" />
        </span>
        <span className="hm-theme-preview__half" data-scheme="dark">
          <span className="hm-theme-preview__line" />
        </span>
      </span>
    );
  }

  return (
    <span className="hm-theme-preview" data-scheme={preference} aria-hidden="true">
      <span className="hm-theme-preview__rail" />
      <span className="hm-theme-preview__body">
        <span className="hm-theme-preview__line" />
        <span className="hm-theme-preview__bubble" />
      </span>
    </span>
  );
}
