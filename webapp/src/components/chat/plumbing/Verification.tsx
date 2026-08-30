import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { isolateLtr } from "../../../i18n/bidi";
import type { ChangedMember, OwnDevicePrompt, VerificationLevel } from "../../../mls/types";

/**
 * UNDESIGNED SURFACES — plain semantic HTML, no styling beyond structure.
 *
 * `docs/design/STATUS.md` carries three `PENDING` rows for this slice
 * (verification sheet, changed-key warning, own-account prompt) and no artboard
 * exists yet, so per CLAUDE.md's UI pipeline this is functional plumbing only.
 * When the design lands these are reskinned to match it exactly.
 *
 * Two things here are **not** styling and must survive the reskin, because
 * ADR 008 and BRIEFS.md make them security properties rather than taste:
 *
 *  - `pinned` and `verified` must not read alike. A pin is this browser
 *    recording what it first saw, or a human saying "I checked"; only
 *    `verified` means two people compared the number out of band.
 *  - There are exactly two exits and no third. No dismiss, no "not now", no
 *    timeout — ADR 008 calls each of those "silently encrypt to the new key
 *    wearing a delay".
 */

/* ── the sheet ─────────────────────────────────────────────────────────── */

interface VerificationSheetProps {
  userId: string;
  name: string;
  level: VerificationLevel | null;
  safetyNumberFor: (userId: string) => Promise<string | null>;
  onVerify: (userId: string) => void;
  onAccept: (userId: string) => void;
  onClose: () => void;
}

export function VerificationSheet({
  userId,
  name,
  level,
  safetyNumberFor,
  onVerify,
  onAccept,
  onClose,
}: VerificationSheetProps) {
  const { t } = useTranslation();
  /* Null while the number is still being worked out; `{ number: null }` once
   * it has settled on "there isn't one". The caller remounts this component
   * per person (`key`), so there is no stale value to clear on the way in. */
  const [settled, setSettled] = useState<{ number: string | null } | null>(null);

  useEffect(() => {
    let live = true;
    safetyNumberFor(userId).then(
      (value) => {
        if (live) {
          setSettled({ number: value });
        }
      },
      (error: unknown) => {
        console.warn("Could not compute the safety number:", error);
        if (live) {
          setSettled({ number: null });
        }
      },
    );
    return () => {
      live = false;
    };
  }, [userId, safetyNumberFor]);

  const number = settled?.number ?? null;

  return (
    <section
      className="hm-plumbing"
      role="dialog"
      aria-modal="false"
      aria-label={t("chat.e2ee.verification.sheetTitle", { name })}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
    >
      <h2>{t("chat.e2ee.verification.sheetTitle", { name })}</h2>

      <p>{t(`chat.e2ee.verification.level.${level ?? "unverified"}`)}</p>

      <p>{t("chat.e2ee.verification.instructions")}</p>

      {/* The number is generated as ASCII digits and never passes through
          Intl, so it reads the same in both locales — a number read aloud in
          Persian has to be the string the other person is looking at. The LTR
          isolate keeps the twelve groups in order inside an RTL paragraph. */}
      {number !== null ? (
        <p>
          <output aria-label={t("chat.e2ee.verification.numberLabel")} dir="ltr">
            {isolateLtr(number)}
          </output>
        </p>
      ) : (
        <p>
          {settled === null
            ? t("chat.e2ee.verification.numberLoading")
            : t("chat.e2ee.verification.numberUnavailable")}
        </p>
      )}

      {/* Exit 1 and exit 2. Nothing else here returns the composer. */}
      <p>
        <button
          type="button"
          disabled={number === null}
          onClick={() => {
            onVerify(userId);
            onClose();
          }}
        >
          {t("chat.e2ee.verification.match")}
        </button>
        <button
          type="button"
          onClick={() => {
            onAccept(userId);
            onClose();
          }}
        >
          {t("chat.e2ee.verification.acceptWithoutComparing")}
        </button>
        <button type="button" onClick={onClose}>
          {t("chat.common.close")}
        </button>
      </p>

      <p>{t("chat.e2ee.verification.acceptIsAPin")}</p>
    </section>
  );
}

/* ── the warning that replaces the composer ────────────────────────────── */

interface VerificationWarningProps {
  changed: readonly ChangedMember[];
  uncoveredLeaves: number;
  /** Non-null when this account's own directory set gained an unaccepted key. */
  own: OwnDevicePrompt | null;
  resolveName: (userId: string) => string | null;
  onCompare: (userId: string) => void;
  onAccept: (userId: string) => void;
  onAcceptOwn: () => void;
  /**
   * The first and last sentences, when this is a call rather than a composer
   * (ADR 009, decision 3 — same records, same two exits, same state).
   *
   * Overridable because only these two differ: a call has to say that the
   * microphone and camera stopped and that this is not a hardware fault,
   * where the composer says sending is paused. Everything between them — who
   * changed, which change it was, and the two exits — is identical, and
   * copying the component to change two lines is how the two drift apart.
   */
  headline?: string;
  continues?: string;
}

export function VerificationWarning({
  changed,
  uncoveredLeaves,
  own,
  resolveName,
  onCompare,
  onAccept,
  onAcceptOwn,
  headline,
  continues,
}: VerificationWarningProps) {
  const { t } = useTranslation();
  const title = headline ?? t("chat.e2ee.verification.blockedTitle");

  return (
    <section className="hm-plumbing" aria-label={title}>
      <p>{title}</p>

      {/* The reader's own account first: it is the loudest thing in the slice,
          because a device nobody here approved is reading everything sent
          after it joins. */}
      {own === null ? null : (
        <div>
          <p>{t("chat.e2ee.verification.ownTitle")}</p>
          <p>{t("chat.e2ee.verification.ownBody", { count: own.keys.length })}</p>
          <p>{t("chat.e2ee.verification.ownIfNotYours")}</p>
          <button type="button" onClick={onAcceptOwn}>
            {t("chat.e2ee.verification.ownAccept")}
          </button>
        </div>
      )}

      {changed.map((member) => {
        // Never the raw id as a fallback: this line asks somebody to make a
        // security judgement about a person, and a UUID is not a person. The
        // generic placeholder is worse copy and better honesty — it says the
        // name is unknown instead of implying that string is one.
        const name = resolveName(member.userId) ?? t("chat.messages.unknownMember");
        return (
          <div key={member.userId}>
            {/* Which of the two it is matters to the person reading it: "a new
                device appeared" and "the key you checked is gone" are
                different events. */}
            <p>{t(`chat.e2ee.verification.${member.kind}`, { name })}</p>
            <button
              type="button"
              onClick={() => {
                onCompare(member.userId);
              }}
            >
              {t("chat.e2ee.verification.compare")}
            </button>
            <button
              type="button"
              onClick={() => {
                onAccept(member.userId);
              }}
            >
              {t("chat.e2ee.verification.accept")}
            </button>
          </div>
        );
      })}

      {/* Nothing to press: the next reconcile's sweep removes this leaf, and
          saying so is more useful than a button that cannot help. */}
      {uncoveredLeaves > 0 ? <p>{t("chat.e2ee.verification.uncovered")}</p> : null}

      <p>{continues ?? t("chat.e2ee.verification.readingContinues")}</p>
    </section>
  );
}
