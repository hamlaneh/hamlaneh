import { useState } from "react";
import { useTranslation } from "react-i18next";

import { formatFullDate } from "../../../chat/format";
import { isolateLtr } from "../../../i18n/bidi";
import type { MlsBackupState, OpenBackupOutcome, RestoreFailure } from "../../../mls/types";

/**
 * UNDESIGNED SURFACES — plain semantic HTML, no styling beyond structure.
 *
 * `docs/design/STATUS.md` carries four `PENDING` rows for this slice (the
 * enrolment offer, the show-once ceremony, the restore screen and the no-key
 * failure path) and no artboard exists yet, so per CLAUDE.md's UI pipeline
 * this is functional plumbing only. When the design lands these are reskinned
 * to match it exactly.
 *
 * Three things here are **not** styling and must survive the reskin, because
 * ADR 010 makes each a property rather than taste:
 *
 *  - **The key is shown once.** Nothing keeps a copy — not this component,
 *    not the keystore, not the server. A design that adds "show it again
 *    later" is asking for a copy to exist.
 *  - **The offer is an offer.** A backup sealed under a key the person never
 *    saw is unrestorable, so it cannot be on by default; and a decline is
 *    recorded and respected rather than re-asked.
 *  - **The no-key path does not lie and does not point at support.** Nobody
 *    can open the blob without the key, and saying otherwise — even by
 *    implication, even with "contact your administrator" — would be the one
 *    dishonest sentence in the whole feature.
 */

interface BackupSurfacesProps {
  backup: MlsBackupState;
  /** The offer only appears once encryption is actually running. */
  deviceReady: boolean;
  onEnable: () => Promise<string | null>;
  onDecline: () => void;
  onOpen: (recoveryKey: string) => Promise<OpenBackupOutcome>;
  onApply: () => Promise<boolean>;
  onDiscard: () => void;
}

/** Which surface is on screen. Null is the ordinary case: none of them. */
type Screen = "offer" | "ceremony" | "restore" | "noKey";

export function BackupSurfaces({
  backup,
  deviceReady,
  onEnable,
  onDecline,
  onOpen,
  onApply,
  onDiscard,
}: BackupSurfacesProps) {
  const { t, i18n } = useTranslation();
  /* Null means "follow the service": the offer shows itself when the service
   * says this account has made no decision. A screen set here overrides that
   * for the rest of the session — including "dismissed", which is how the
   * ceremony closes without turning into a second offer. */
  const [screen, setScreen] = useState<Screen | "dismissed" | null>(null);
  const [recoveryKey, setRecoveryKey] = useState<string | null>(null);
  const [entry, setEntry] = useState("");
  const [failure, setFailure] = useState<RestoreFailure | null>(null);
  const [busy, setBusy] = useState(false);

  const showing: Screen | "dismissed" | null =
    screen ?? (deviceReady && backup.status === "offer" ? "offer" : null);

  if (showing === null || showing === "dismissed") {
    return null;
  }

  const close = () => {
    setScreen("dismissed");
    setFailure(null);
    setEntry("");
  };

  if (showing === "offer") {
    return (
      <section className="hm-plumbing" aria-label={t("chat.e2ee.backup.offerTitle")}>
        <h2>{t("chat.e2ee.backup.offerTitle")}</h2>
        <p>{t("chat.e2ee.backup.offerSaves")}</p>
        {/* Said plainly, because the opposite is what people assume: this is
            not a backup of your messages, and it never becomes one silently. */}
        <p>{t("chat.e2ee.backup.offerNotMessages")}</p>
        <p>{t("chat.e2ee.backup.offerShownOnce")}</p>
        <p>{t("chat.e2ee.backup.offerLosingIt")}</p>
        <p>
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              void onEnable()
                .then((key) => {
                  if (key === null) {
                    setFailure("failed");
                    return;
                  }
                  setRecoveryKey(key);
                  setScreen("ceremony");
                })
                .finally(() => {
                  setBusy(false);
                });
            }}
          >
            {t("chat.e2ee.backup.offerSetUp")}
          </button>
          <button
            type="button"
            onClick={() => {
              setScreen("restore");
            }}
          >
            {t("chat.e2ee.backup.offerHaveKey")}
          </button>
          <button
            type="button"
            onClick={() => {
              onDecline();
              close();
            }}
          >
            {t("chat.e2ee.backup.offerNotNow")}
          </button>
        </p>
        {failure === null ? null : <p role="alert">{t("chat.e2ee.backup.setUpFailed")}</p>}
      </section>
    );
  }

  if (showing === "ceremony") {
    return (
      <section className="hm-plumbing" aria-label={t("chat.e2ee.backup.ceremonyTitle")}>
        <h2>{t("chat.e2ee.backup.ceremonyTitle")}</h2>
        <p>{t("chat.e2ee.backup.ceremonyBody")}</p>
        {/* Crockford base32 in groups of four, direction-pinned LTR: it is
            typed back character for character on another device, so it has to
            read in the same order in both locales. */}
        <p>
          <output aria-label={t("chat.e2ee.backup.keyLabel")} dir="ltr">
            {isolateLtr(recoveryKey ?? "")}
          </output>
        </p>
        <p>{t("chat.e2ee.backup.ceremonyOnce")}</p>
        <p>
          <button
            type="button"
            onClick={() => {
              // The key leaves this component here and exists nowhere else.
              setRecoveryKey(null);
              close();
            }}
          >
            {t("chat.e2ee.backup.ceremonyDone")}
          </button>
        </p>
      </section>
    );
  }

  if (showing === "noKey") {
    return (
      <section className="hm-plumbing" aria-label={t("chat.e2ee.backup.noKeyTitle")}>
        <h2>{t("chat.e2ee.backup.noKeyTitle")}</h2>
        {/* ADR 010, decision 2, near enough word for word. Every sentence is
            load-bearing and none of them may soften into "ask support". */}
        <p>{t("chat.e2ee.backup.noKeyCannotOpen")}</p>
        <p>{t("chat.e2ee.backup.noKeyNobodyHasIt")}</p>
        <p>{t("chat.e2ee.backup.noKeyWhatContinues")}</p>
        <p>{t("chat.e2ee.backup.noKeyByDesign")}</p>
        <p>
          <button type="button" onClick={close}>
            {t("chat.common.close")}
          </button>
        </p>
      </section>
    );
  }

  const pending = backup.pending;
  return (
    <section className="hm-plumbing" aria-label={t("chat.e2ee.backup.restoreTitle")}>
      <h2>{t("chat.e2ee.backup.restoreTitle")}</h2>

      {pending === null ? (
        <>
          <p>{t("chat.e2ee.backup.restoreBody")}</p>
          <p>
            <label>
              {t("chat.e2ee.backup.keyLabel")}
              {/* Never `autocomplete="password"` and never a password field:
                  a manager offering to save this would be storing the one
                  thing the design says is not stored anywhere. */}
              <input
                type="text"
                dir="ltr"
                autoComplete="off"
                spellCheck={false}
                value={entry}
                onChange={(event) => {
                  setEntry(event.target.value);
                  setFailure(null);
                }}
              />
            </label>
          </p>
          <p>
            <button
              type="button"
              disabled={busy || entry.trim() === ""}
              onClick={() => {
                setBusy(true);
                void onOpen(entry)
                  .then((outcome) => {
                    setFailure(outcome.status === "refused" ? outcome.reason : null);
                  })
                  .finally(() => {
                    setBusy(false);
                  });
              }}
            >
              {t("chat.e2ee.backup.restoreSubmit")}
            </button>
            <button
              type="button"
              onClick={() => {
                setScreen("noKey");
              }}
            >
              {t("chat.e2ee.backup.restoreNoKey")}
            </button>
            <button type="button" onClick={close}>
              {t("chat.common.close")}
            </button>
          </p>
          {failure === null ? null : (
            <p role="alert">{t(`chat.e2ee.backup.failure.${failure}`)}</p>
          )}
        </>
      ) : (
        <>
          {/* The sealed date, offered as a fact to confirm. It is the only
              freshness check a fresh device has — the server is the party
              under test, and the human is the other channel (ADR 010,
              decision 3) — and the screen says so rather than presenting it
              as a formality. */}
          <p>
            {t("chat.e2ee.backup.restoreSealedOn", {
              date: formatFullDate(pending.createdAt, i18n.language),
            })}
          </p>
          <p>{t("chat.e2ee.backup.restoreConfirmDate")}</p>
          <p>{t("chat.e2ee.backup.restoreRecords", { count: pending.records })}</p>
          <p>{t("chat.e2ee.backup.restoreWhatComesBack")}</p>
          <p>
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                setBusy(true);
                void onApply()
                  .then(() => {
                    close();
                  })
                  .finally(() => {
                    setBusy(false);
                  });
              }}
            >
              {t("chat.e2ee.backup.restoreConfirm")}
            </button>
            <button
              type="button"
              onClick={() => {
                onDiscard();
                close();
              }}
            >
              {t("chat.e2ee.backup.restoreReject")}
            </button>
          </p>
        </>
      )}
    </section>
  );
}

/**
 * The quiet indicator ADR 010 asks for, and only when it has something to say:
 * a decline re-surfaced passively, and an upload that is not landing.
 *
 * Deliberately silent for "on" and "offer". "Offer" already has a whole
 * surface above; a standing "your backup is working" line would be the nag the
 * ADR rules out, and where a permanent status line belongs is a question for
 * the artboard rather than for this file.
 */
export function BackupIndicator({ backup }: { backup: MlsBackupState }) {
  const { t } = useTranslation();
  if (backup.writeFailed) {
    return <p role="status">{t("chat.e2ee.backup.writeFailed")}</p>;
  }
  if (backup.status === "declined") {
    return <p role="status">{t("chat.e2ee.backup.declinedNote")}</p>;
  }
  return null;
}
