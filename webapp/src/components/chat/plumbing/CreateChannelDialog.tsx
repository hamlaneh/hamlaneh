import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { bornEncrypted } from "../../../instance/encryptionMode";
import type { EncryptionMode } from "../../../instance/encryptionMode";
import { creationRefusalKey } from "./creationRefusal";
import type { CreationRefusalKey } from "./creationRefusal";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The mockup has no create-channel screen, so per the UI pipeline
 * (CLAUDE.md -> docs/design/STATUS.md) this is functional plumbing only; it is
 * reskinned when a design lands.
 *
 * Copy note: the kind choice deliberately does not promise that a public
 * channel is browsable. In Phase 1.2 visibility is membership for every kind —
 * there is no channel directory — so "public" only means it may later be
 * joinable, and the helper text says exactly that and nothing more.
 */

interface CreateChannelDialogProps {
  /**
   * The organisation's encryption mode. It decides what this channel is born
   * as; there is no per-channel choice, because one would be a hole in exactly
   * the guarantee the mode exists to state (ADR 011 decision 1).
   */
  mode: EncryptionMode;
  /** Resolves to null on success, or the server's own refusal code. */
  onCreate: (slug: string, kind: "public" | "private", e2ee: boolean) => Promise<string | null>;
  onClose: () => void;
}

export function CreateChannelDialog({ mode, onCreate, onClose }: CreateChannelDialogProps) {
  const { t } = useTranslation();
  const [slug, setSlug] = useState("");
  const [kind, setKind] = useState<"public" | "private">("public");
  const [failureKey, setFailureKey] = useState<
    CreationRefusalKey | "chat.createChannel.failed" | null
  >(null);
  const slugId = useId();

  const e2ee = bornEncrypted(mode);

  return (
    <section
      className="hm-plumbing"
      role="dialog"
      aria-modal="true"
      aria-label={t("chat.createChannel.title")}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
    >
      <h2>{t("chat.createChannel.title")}</h2>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void onCreate(slug.trim(), kind, e2ee).then((refusal) => {
            if (refusal === null) {
              onClose();
            } else {
              setFailureKey(creationRefusalKey(refusal, "chat.createChannel.failed"));
            }
          });
        }}
      >
        <p>
          <label htmlFor={slugId}>{t("chat.createChannel.nameLabel")}</label>
          <input
            id={slugId}
            name="slug"
            value={slug}
            autoFocus
            onChange={(event) => {
              setSlug(event.target.value);
            }}
          />
        </p>
        <fieldset>
          <legend>{t("chat.createChannel.kindLabel")}</legend>
          <p>
            <label>
              <input
                type="radio"
                name="kind"
                value="public"
                checked={kind === "public"}
                onChange={() => {
                  setKind("public");
                }}
              />
              {t("chat.createChannel.public")}
            </label>
          </p>
          <p>
            <label>
              <input
                type="radio"
                name="kind"
                value="private"
                checked={kind === "private"}
                onChange={() => {
                  setKind("private");
                }}
              />
              {t("chat.createChannel.private")}
            </label>
          </p>
          <p>{t("chat.createChannel.visibilityNote")}</p>
        </fieldset>
        {/* Not a checkbox any more: the organisation's mode decides, and the
            server refuses a request that disagrees with it. Offering a choice
            the server would refuse is offering a refusal, so what is shown is
            the outcome — stated, because it is fixed at creation and can never
            be changed for this channel afterwards. */}
        <p>{t(`chat.createChannel.e2eeByMode.${mode}`)}</p>
        {e2ee ? <p>{t("chat.createChannel.e2eeNote")}</p> : null}
        {failureKey === null ? null : <p role="alert">{t(failureKey)}</p>}
        <p>
          <button type="submit">{t("chat.createChannel.submit")}</button>
          <button type="button" onClick={onClose}>
            {t("chat.common.cancel")}
          </button>
        </p>
      </form>
    </section>
  );
}
