import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { bornEncrypted } from "../../../instance/encryptionMode";
import type { EncryptionMode } from "../../../instance/encryptionMode";
import { useFocusTrap } from "../../settings/useFocusTrap";
import {
  CircleAlertIcon,
  HashIcon,
  InfoIcon,
  LoaderCircleIcon,
  LockIcon,
  XIcon,
} from "../../icons";
import { creationRefusalKey } from "./creationRefusal";
import type { CreationRefusalKey } from "./creationRefusal";

/**
 * Create a channel — `chat-addendum-create-channel-light` / `-dark` /
 * `-states`, with the contract on `chat-addendum-overlay-components` §03.
 *
 * A 560-wide modal at z-index 4 over the z-index 3 scrim, the same layering
 * the mobile drawer uses. Everything the sheet fixes and an implementer must
 * not guess is here:
 *
 *  - the submitted payload is exactly a slug and a kind — no topic, no
 *    description, no members, no discoverability;
 *  - the visible `#` is decoration and never part of the value;
 *  - the slug is 2-64 of `^[a-z0-9][a-z0-9_-]*$` and an isolated LTR run even
 *    inside the Persian UI;
 *  - the primary action is disabled while empty, invalid or submitting, sits
 *    in a fixed-width button so "Creating…" cannot resize it, and cannot fire
 *    twice;
 *  - an invalid name is a field-level error beside the input; a request
 *    failure is a banner that preserves both the name and the kind.
 *
 * Copy note kept from the plumbing build, because the design agrees with it:
 * no string here promises that a public channel is browsable. In Phase 1.2
 * visibility is membership for every kind.
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

/** overlay-components §03, verbatim. */
const SLUG_PATTERN = /^[a-z0-9][a-z0-9_-]*$/;
const SLUG_MIN = 2;
const SLUG_MAX = 64;

function slugIsValid(slug: string): boolean {
  return slug.length >= SLUG_MIN && slug.length <= SLUG_MAX && SLUG_PATTERN.test(slug);
}

export function CreateChannelDialog({ mode, onCreate, onClose }: CreateChannelDialogProps) {
  const { t } = useTranslation();
  const [slug, setSlug] = useState("");
  const [kind, setKind] = useState<"public" | "private">("public");
  const [submitting, setSubmitting] = useState(false);
  /** Errors only after the field has been left or a submit attempted. */
  const [showInvalid, setShowInvalid] = useState(false);
  const [failureKey, setFailureKey] = useState<
    CreationRefusalKey | "chat.createChannel.failed" | null
  >(null);
  const slugId = useId();
  const titleId = useId();
  const errorId = useId();
  const helperId = useId();

  const dialogRef = useRef<HTMLDivElement>(null);
  const fieldRef = useRef<HTMLInputElement>(null);
  const onTabKeyDown = useFocusTrap(dialogRef);

  // Initial focus enters the name field (overlay-components §03). The trap's
  // own effect lands on the dialog first; this one runs after it.
  useEffect(() => {
    fieldRef.current?.focus();
  }, []);

  const valid = slugIsValid(slug);
  const invalidVisible = showInvalid && !valid && slug !== "";

  const submit = () => {
    // Disabled and guarded: the second press of a double press does nothing
    // even if the button has not repainted yet.
    if (!valid || submitting) {
      setShowInvalid(true);
      return;
    }
    setSubmitting(true);
    setFailureKey(null);
    void onCreate(slug, kind, bornEncrypted(mode)).then(
      (refusal) => {
        if (refusal === null) {
          onClose();
          return;
        }
        // The name and the kind both survive a failure — nothing is cleared.
        setSubmitting(false);
        /*
         * CONFLICT, reported to the orchestrator and deliberately not
         * resolved here: the sheet says a duplicate name is a field-level
         * error beside the input, which means `channel_slug_taken` should map
         * to `chat.createChannel.duplicate`. `creationMode.test.tsx` ->
         * "keeps its own message for a failure the mode does not explain"
         * uses that exact code to assert the generic banner instead. The test
         * wins until somebody decides; the key exists and is unwired.
         */
        setFailureKey(creationRefusalKey(refusal, "chat.createChannel.failed"));
      },
      () => {
        setSubmitting(false);
        setFailureKey("chat.createChannel.failed");
      },
    );
  };

  const kinds = [
    {
      value: "public" as const,
      label: t("chat.createChannel.public"),
      note: t("chat.createChannel.publicNote"),
      glyph: <HashIcon size={15} strokeWidth={1.85} />,
    },
    {
      value: "private" as const,
      label: t("chat.createChannel.private"),
      note: t("chat.createChannel.privateNote"),
      glyph: <LockIcon size={15} strokeWidth={1.85} />,
    },
  ];

  return (
    <>
      {/* Dismissal by backdrop press, exactly as Escape and Cancel do. */}
      <button
        type="button"
        className="hm-overlay-scrim"
        aria-label={t("chat.common.close")}
        onClick={onClose}
      />
      <div
        ref={dialogRef}
        className="hm-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            onClose();
            return;
          }
          onTabKeyDown(event);
        }}
      >
        <div className="hm-dialog__header">
          <div className="hm-dialog__heading">
            <h2 className="hm-dialog__title" id={titleId}>
              {t("chat.createChannel.title")}
            </h2>
          </div>
          <button
            type="button"
            className="hm-icon-button hm-close-button"
            aria-label={t("chat.common.close")}
            onClick={onClose}
          >
            <XIcon size={19} strokeWidth={1.85} />
          </button>
        </div>

        <form
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <div className="hm-dialog__body">
            <div className="hm-field">
              <label className="hm-field__label" htmlFor={slugId}>
                {t("chat.createChannel.nameLabel")}
              </label>
              <div className="hm-input-shell" data-invalid={invalidVisible}>
                {/* Decoration. It is not part of the submitted value. */}
                <span className="hm-input-shell__prefix" aria-hidden="true">
                  #
                </span>
                <input
                  ref={fieldRef}
                  id={slugId}
                  className="hm-input-shell__field hm-input-shell__field--slug"
                  name="slug"
                  /* A slug is a technical string: one isolated LTR run, in the
                     Persian UI too (overlay-components §05). */
                  dir="ltr"
                  value={slug}
                  maxLength={SLUG_MAX}
                  aria-invalid={invalidVisible}
                  aria-describedby={invalidVisible ? errorId : helperId}
                  onChange={(event) => {
                    setSlug(event.target.value);
                  }}
                  onBlur={() => {
                    setShowInvalid(true);
                  }}
                />
              </div>
              {invalidVisible ? (
                <p className="hm-field__error" id={errorId} role="alert">
                  <CircleAlertIcon size={14} strokeWidth={2} />
                  {t("chat.createChannel.invalidSlug")}
                </p>
              ) : (
                <p className="hm-field__helper" id={helperId}>
                  {t("chat.createChannel.nameHelper")}
                </p>
              )}
            </div>

            <fieldset className="hm-choice-set">
              <legend className="hm-choice-set__legend">
                {t("chat.createChannel.kindLabel")}
              </legend>
              <div className="hm-choice-row">
                {kinds.map((option) => (
                  <label key={option.value} className="hm-choice">
                    <input
                      className="hm-choice__input"
                      type="radio"
                      name="kind"
                      value={option.value}
                      aria-label={option.label}
                      checked={kind === option.value}
                      onChange={() => {
                        setKind(option.value);
                      }}
                    />
                    <span className="hm-choice__mark" aria-hidden="true" />
                    <span className="hm-choice__text">
                      <span className="hm-choice__name">
                        {option.glyph}
                        {option.label}
                      </span>
                      <span className="hm-choice__note">{option.note}</span>
                    </span>
                  </label>
                ))}
              </div>
              {/* Persistent, never a tooltip: it is the one sentence that stops
                  "public" from being read as browsable. */}
              <p className="hm-note">
                <span className="hm-note__glyph">
                  <InfoIcon size={16} strokeWidth={1.85} />
                </span>
                {t("chat.createChannel.visibilityNote")}
              </p>
            </fieldset>

            {/* Not a checkbox any more: the organisation's mode decides, and the
                server refuses a request that disagrees with it. Offering a choice
                the server would refuse is offering a refusal, so what is shown is
                the outcome — stated, because it is fixed at creation and can never
                be changed for this channel afterwards. */}
            <p className="hm-field__helper">{t(`chat.createChannel.e2eeByMode.${mode}`)}</p>
            {bornEncrypted(mode) ? (
              <p className="hm-field__helper">{t("chat.createChannel.e2eeNote")}</p>
            ) : null}

            {failureKey === null ? null : (
              <p className="hm-banner" role="alert">
                <span className="hm-banner__glyph">
                  <CircleAlertIcon size={16} strokeWidth={1.85} />
                </span>
                {t(failureKey)}
              </p>
            )}
          </div>

          <div className="hm-dialog__footer">
            <button
              type="submit"
              className="hm-button hm-button--primary hm-button--fixed"
              disabled={!valid || submitting}
            >
              {submitting ? (
                <>
                  <LoaderCircleIcon size={16} className="hm-spin" />
                  {t("chat.createChannel.submitting")}
                </>
              ) : (
                t("chat.createChannel.submit")
              )}
            </button>
            <button type="button" className="hm-button" onClick={onClose}>
              {t("chat.common.cancel")}
            </button>
          </div>
        </form>
      </div>
    </>
  );
}
