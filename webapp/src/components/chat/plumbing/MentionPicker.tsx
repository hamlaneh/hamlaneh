import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../../../api/client";
import type { UserSummary } from "../../../chat/types";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The mockup draws no mention picker. What matters here is the wire format:
 * the inserted token is `<@{user_id}>` (openapi.yaml -> SendMessageRequest),
 * never a display name — names are neither unique nor stable, and in Persian
 * they cannot match the username pattern at all.
 */

interface MentionPickerProps {
  channelId: string;
  onInsert: (token: string) => void;
}

export function MentionPicker({ channelId, onInsert }: MentionPickerProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [members, setMembers] = useState<UserSummary[]>([]);

  // One in-flight member request at a time; a reopen or an unmount cancels it.
  const inFlight = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      inFlight.current?.abort();
    },
    [],
  );

  const load = () => {
    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    void (async () => {
      try {
        const { data } = await api.GET("/api/v1/channels/{channelId}/members", {
          params: { path: { channelId }, query: { limit: 100 } },
          signal: controller.signal,
        });
        setMembers(data?.members ?? []);
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        console.warn("Could not load channel members:", error);
        setMembers([]);
      }
    })();
  };

  return (
    <div>
      <button
        type="button"
        onClick={() => {
          const next = !open;
          setOpen(next);
          if (next) {
            load();
          }
        }}
        aria-expanded={open}
      >
        {t("chat.composer.mention")}
      </button>
      {open ? (
        <ul>
          {members.map((member) => (
            <li key={member.id}>
              <button
                type="button"
                onClick={() => {
                  onInsert(`<@${member.id}>`);
                  setOpen(false);
                }}
              >
                {member.display_name}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
