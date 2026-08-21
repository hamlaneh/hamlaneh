import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

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
  onCreate: (slug: string, kind: "public" | "private") => Promise<boolean>;
  onClose: () => void;
}

export function CreateChannelDialog({ onCreate, onClose }: CreateChannelDialogProps) {
  const { t } = useTranslation();
  const [slug, setSlug] = useState("");
  const [kind, setKind] = useState<"public" | "private">("public");
  const [failed, setFailed] = useState(false);
  const slugId = useId();

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
          void onCreate(slug.trim(), kind).then((ok) => {
            if (ok) {
              onClose();
            } else {
              setFailed(true);
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
        {failed ? <p role="alert">{t("chat.createChannel.failed")}</p> : null}
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
