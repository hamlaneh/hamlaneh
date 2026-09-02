import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { Channel } from "../../../chat/types";
import { HashIcon, LoaderCircleIcon, LockIcon, UserPlusIcon, XIcon } from "../../icons";
import { CHANNEL_MENU_ID, useRestoreFocus } from "./overlay";

/**
 * Channel actions — `chat-addendum-channel-menu-light` / `-dark` / `-rtl-fa` /
 * `-mobile` / `-states`, with the contract on
 * `chat-addendum-menu-components` §01–§02.
 *
 * An **anchored non-modal dialog, not an ARIA menu**: it holds a form, which
 * is something a menu cannot. So `role="dialog"` with `aria-modal="false"`,
 * normal DOM tab order, no roving tabindex, no arrow-key menu navigation, no
 * scrim and no desktop focus trap. Tab order is Close, Invite people, Topic,
 * Save topic; focus enters Topic; Escape and the visible Close restore focus
 * to the trigger.
 *
 * The topic contract, none of which is guessable:
 *   - 250 maximum; empty deliberately clears it, and the helper says so;
 *   - the authored value is preserved exactly — never trimmed or normalised;
 *   - Save is disabled while the draft equals the current topic, and returning
 *     to the exact original restores the pristine state;
 *   - the counter is ASCII, `aria-live="off"`, and never announces per key;
 *   - success announces politely and closes; failure preserves the exact draft
 *     and allows retry, and editing clears the stale error;
 *   - a dirty draft is never discarded silently: every dismissal path turns
 *     this same anchored surface into the discard confirmation, which uses
 *     hierarchy and copy and never accent red.
 */

interface ChannelMenuProps {
  channel: Channel;
  onInvite: () => void;
  onSetTopic: (topic: string) => Promise<boolean>;
  /** Opens the safety-number sheet. Absent when this is not an encrypted DM. */
  onVerify?: (() => void) | undefined;
  onClose: () => void;
}

const TOPIC_MAX = 250;

export function ChannelMenu({
  channel,
  onInvite,
  onSetTopic,
  onVerify,
  onClose,
}: ChannelMenuProps) {
  const { t } = useTranslation();
  const [topic, setTopic] = useState(channel.topic);
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState(false);
  /** Set when a dismissal was requested while the draft was dirty. */
  const [pendingExit, setPendingExit] = useState<null | "close" | "invite">(null);
  const topicId = useId();
  const titleId = useId();
  const errorId = useId();

  const popoverRef = useRef<HTMLDivElement>(null);
  const topicRef = useRef<HTMLInputElement>(null);
  useRestoreFocus(popoverRef);

  // Focus enters Topic (menu-components §02). A DM has no topic field, so it
  // lands on the surface itself instead.
  useEffect(() => {
    (topicRef.current ?? popoverRef.current)?.focus();
  }, []);

  const dm = channel.kind === "dm";
  const dirty = !dm && topic !== channel.topic;
  const tooLong = topic.length > TOPIC_MAX;

  /** Every dismissal path routes through here, so none of them can lose a draft. */
  const leave = (destination: "close" | "invite") => {
    if (dirty && !saving) {
      setPendingExit(destination);
      return;
    }
    if (destination === "invite") {
      onInvite();
    } else {
      onClose();
    }
  };

  const save = () => {
    if (!dirty || tooLong || saving) {
      return;
    }
    setSaving(true);
    setFailed(false);
    // The authored value goes as authored: no trim, no normalisation.
    void onSetTopic(topic).then(
      (ok) => {
        setSaving(false);
        if (ok) {
          // The polite "Topic saved." announcement belongs to the shell's
          // status region: this surface is gone by the time it would speak.
          onClose();
        } else {
          setFailed(true);
        }
      },
      () => {
        setSaving(false);
        setFailed(true);
      },
    );
  };

  const header = (
    <div className="hm-popover__header">
      <div className="hm-popover__heading">
        <h2 className="hm-popover__title" id={titleId}>
          {t("chat.header.channelActions")}
        </h2>
        {dm ? null : (
          <p className="hm-popover__subject">
            {channel.kind === "private" ? (
              <LockIcon size={13} strokeWidth={1.85} />
            ) : (
              <HashIcon size={13} strokeWidth={1.85} />
            )}
            {/* The complete slug is one isolated LTR unit — never "deploys#". */}
            <bdi className="hm-slug" dir="ltr">
              {`#${channel.slug ?? ""}`}
            </bdi>
            <span className="hm-tick" aria-hidden="true" />
            {/* Public and private have identical permissions and identical
                actions; only the glyph and this word distinguish them. */}
            <span className="hm-popover__kind">
              {channel.kind === "private"
                ? t("chat.createChannel.private")
                : t("chat.createChannel.public")}
            </span>
          </p>
        )}
      </div>
      <button
        type="button"
        className="hm-icon-button hm-close-button"
        aria-label={t("chat.common.close")}
        onClick={() => {
          leave("close");
        }}
      >
        <XIcon size={18} strokeWidth={1.85} />
      </button>
    </div>
  );

  return (
    <div
      ref={popoverRef}
      id={CHANNEL_MENU_ID}
      className="hm-popover hm-popover--channel"
      role="dialog"
      aria-modal="false"
      aria-labelledby={titleId}
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.key !== "Escape") {
          return;
        }
        event.stopPropagation();
        if (pendingExit !== null) {
          // Escape from the confirmation means Keep editing.
          setPendingExit(null);
          topicRef.current?.focus();
          return;
        }
        leave("close");
      }}
    >
      {header}

      {pendingExit !== null ? (
        <div className="hm-discard">
          <p className="hm-popover__title">{t("chat.channelMenu.discardTitle")}</p>
          <p className="hm-discard__body">{t("chat.channelMenu.discardBody")}</p>
          <div className="hm-discard__actions">
            <button
              type="button"
              className="hm-button hm-button--primary"
              onClick={() => {
                setPendingExit(null);
                topicRef.current?.focus();
              }}
            >
              {t("chat.channelMenu.keepEditing")}
            </button>
            {/* Hierarchy and copy carry this, never accent red: red stays
                reserved for message deletion. */}
            <button
              type="button"
              className="hm-button"
              onClick={() => {
                if (pendingExit === "invite") {
                  onInvite();
                } else {
                  onClose();
                }
              }}
            >
              {t("chat.channelMenu.discardChanges")}
            </button>
          </div>
        </div>
      ) : dm ? (
        <div className="hm-popover__body">
          <p className="hm-field__helper">{t("chat.channelMenu.dmNote")}</p>
          {/* The way in when nothing is wrong. Without it the ceremony would be
              reachable only from the warning, so nobody could verify anybody
              until a key had already changed — which is backwards. */}
          {onVerify === undefined ? null : (
            <button type="button" className="hm-button" onClick={onVerify}>
              {t("chat.e2ee.verification.openSheet")}
            </button>
          )}
        </div>
      ) : (
        <div className="hm-popover__body">
          <button
            type="button"
            className="hm-menu-action hm-menu-action--outlined"
            onClick={() => {
              leave("invite");
            }}
          >
            <span className="hm-menu-action__glyph">
              <UserPlusIcon size={17} strokeWidth={1.85} />
            </span>
            {t("chat.empty.invite")}
          </button>

          <span className="hm-rule" aria-hidden="true" />

          <form
            className="hm-field"
            onSubmit={(event) => {
              event.preventDefault();
              save();
            }}
          >
            <label className="hm-field__label" htmlFor={topicId}>
              {t("chat.channelMenu.topicLabel")}
            </label>
            <input
              ref={topicRef}
              id={topicId}
              className="hm-topic__field"
              name="topic"
              dir="auto"
              value={topic}
              disabled={saving}
              aria-invalid={tooLong}
              aria-describedby={tooLong || failed ? errorId : undefined}
              onChange={(event) => {
                setTopic(event.target.value);
                // Editing clears the stale error.
                setFailed(false);
              }}
            />
            <div className="hm-topic__meta">
              <span className="hm-topic__helper">{t("chat.channelMenu.topicHelper")}</span>
              {/* ASCII in both languages, and deliberately not a live region. */}
              <span className="hm-topic__counter" aria-live="off" dir="ltr">
                {t("chat.channelMenu.topicCounter", {
                  used: String(topic.length),
                  limit: String(TOPIC_MAX),
                })}
              </span>
            </div>
            {tooLong ? (
              <p className="hm-field__error" id={errorId} role="alert">
                {t("chat.channelMenu.topicTooLong")}
              </p>
            ) : failed ? (
              // Beside the action it concerns, and it does not steal focus.
              <p className="hm-field__error" id={errorId} role="alert">
                {t("chat.channelMenu.topicSaveFailed")}
              </p>
            ) : null}
            <button
              type="submit"
              className="hm-button hm-button--primary hm-button--fixed-narrow"
              disabled={!dirty || tooLong || saving}
            >
              {saving ? (
                <>
                  <LoaderCircleIcon size={15} className="hm-spin" />
                  {t("chat.channelMenu.saving")}
                </>
              ) : (
                t("chat.channelMenu.saveTopic")
              )}
            </button>
          </form>
        </div>
      )}
    </div>
  );
}
