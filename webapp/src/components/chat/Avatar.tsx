import { avatarInitial, avatarTint } from "../../chat/avatar";
import type { Presence } from "../../chat/types";

interface AvatarProps {
  userId: string;
  displayName: string;
  /** Drawn size in px — 26 in the sidebar and message meta, 32 in the footer. */
  size: number;
  /** Initial type size; the artboards pair 26/10 and 32/13. */
  typeSize?: number;
  presence?: Presence | undefined;
  /**
   * Accessible name for the presence dot. Pass null where a visible presence
   * label already sits beside it (the user footer, the DM header) so the state
   * is not announced twice.
   */
  presenceLabel?: string | null;
}

/**
 * Initials avatar with an optional presence dot. Tint comes from a stable
 * hash of the user id (chat-components -> "Avatar tints"), so one person is
 * always the same colour in every client.
 */
export function Avatar({
  userId,
  displayName,
  size,
  typeSize,
  presence,
  presenceLabel,
}: AvatarProps) {
  const style = {
    "--hm-avatar-size": `${String(size)}px`,
    ...(typeSize === undefined ? {} : { "--hm-avatar-type": `${String(typeSize)}px` }),
  } as React.CSSProperties;

  return (
    <span className="hm-avatar" data-tint={avatarTint(userId)} style={style}>
      <span className="hm-avatar__initial" aria-hidden="true">
        {avatarInitial(displayName)}
      </span>
      {presence === undefined ? null : (
        <span
          className="hm-presence"
          data-state={presence}
          {...(presenceLabel === null || presenceLabel === undefined
            ? { "aria-hidden": true }
            : { role: "img", "aria-label": presenceLabel })}
        >
          {/* The bar is what makes "away" readable without colour. */}
          {presence === "away" ? <span className="hm-presence__bar" /> : null}
        </span>
      )}
    </span>
  );
}
