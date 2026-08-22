interface RecoveryCodeListProps {
  codes: readonly string[];
  /** Names the list for assistive technology. */
  label: string;
}

/**
 * The ten recovery codes, `settings-2fa-recovery-codes`.
 *
 * Selectable text in an ordered list, never an image: a screen reader has to
 * be able to read them out and a password manager has to be able to capture
 * them. The drawn "01"–"10" markers are decorative — the list element already
 * numbers its items — so they are hidden from assistive technology to avoid
 * reading every number twice.
 */
export function RecoveryCodeList({ codes, label }: RecoveryCodeListProps) {
  return (
    <ol className="hm-recovery-codes" aria-label={label}>
      {codes.map((code, index) => (
        <li key={code} className="hm-recovery-code">
          <span className="hm-recovery-code__index" aria-hidden="true">
            {String(index + 1).padStart(2, "0")}
          </span>
          {/* A recovery code is an LTR run whatever the page direction. */}
          <span className="hm-recovery-code__value" dir="ltr">
            {code}
          </span>
        </li>
      ))}
    </ol>
  );
}
