import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import "../../i18n";
import type { Channel, Message, UserSummary } from "../../chat/types";
import en from "../../locales/en/common.json";
import { MessageList } from "./MessageList";

/**
 * jsdom implements no layout, so scrollHeight is always 0 and the scroll
 * arithmetic cannot be observed by rendering alone. These stub the two
 * measurements the list reads, which is enough to pin the arithmetic itself.
 */

afterEach(() => {
  vi.restoreAllMocks();
});

const ME: UserSummary = { id: "u-me", username: "me", display_name: "Me" };
const OTHER: UserSummary = { id: "u-other", username: "nasrin", display_name: "Nasrin" };

const CHANNEL: Channel = {
  id: "c-general",
  kind: "public",
  slug: "general",
  topic: "",
  member_count: 4,
  unread_count: 0,
  mention_count: 0,
  created_at: "2026-08-01T09:00:00.000Z",
  created_by: OTHER.id,
};

function message(id: string, createdAt: string): Message {
  return {
    id,
    channel_id: CHANNEL.id,
    author: OTHER,
    client_msg_id: `client-${id}`,
    content: `Backlog note ${id}`,
    created_at: createdAt,
    attachments: [],
  };
}

interface ListOptions {
  messages: Message[];
  loadingOlder: boolean;
  focusMessageId?: string | null;
}

function list({ messages, loadingOlder, focusMessageId = null }: ListOptions) {
  return (
    <MessageList
      channel={CHANNEL}
      channelId={CHANNEL.id}
      messages={messages}
      pending={[]}
      currentUser={ME}
      canModerate={false}
      resolveMention={() => null}
      loading={false}
      loadingOlder={loadingOlder}
      hasMoreOlder
      dividerBeforeId={null}
      focusMessageId={focusMessageId}
      dimmed={false}
      onLoadOlder={() => undefined}
      onEdit={() => Promise.resolve(true)}
      onDelete={() => Promise.resolve(true)}
    />
  );
}

/**
 * Gives the list a content height and a settable scroll position. jsdom
 * reports 0 for both and silently ignores assignments to scrollTop, so
 * without this the arithmetic is invisible.
 */
function stubHeight(element: HTMLElement, scrollHeight: number): void {
  Object.defineProperty(element, "scrollHeight", { value: scrollHeight, configurable: true });
  Object.defineProperty(element, "clientHeight", { value: 400, configurable: true });
  if (!Object.getOwnPropertyDescriptor(element, "scrollTop")?.writable) {
    Object.defineProperty(element, "scrollTop", { value: 0, writable: true, configurable: true });
  }
}

describe("scrollback", () => {
  it("keeps the reader's place when an older page is prepended", () => {
    const today = [message("50", "2026-08-21T09:00:00.000Z")];
    const { rerender } = render(list({ messages: today, loadingOlder: false }));

    const region = screen.getByRole("log");
    stubHeight(region, 1000);
    // One render with the height in place, so the list has a baseline.
    rerender(list({ messages: today, loadingOlder: false }));
    region.scrollTop = 120;

    // The loader and skeleton go in above the conversation, adding 180.
    rerender(list({ messages: today, loadingOlder: true }));
    stubHeight(region, 1180);

    // The older page replaces them: 600 more content than before the fetch.
    stubHeight(region, 1600);
    rerender(list({ messages: [message("1", "2026-08-21T08:00:00.000Z"), ...today], loadingOlder: false }));

    expect(region.scrollTop).toBe(120 + 600);
  });

  it("leaves the scroll position alone when nothing was prepended", () => {
    const messages = [message("50", "2026-08-21T09:00:00.000Z")];
    const { rerender } = render(list({ messages, loadingOlder: false }));

    const region = screen.getByRole("log");
    stubHeight(region, 1000);
    rerender(list({ messages, loadingOlder: false }));
    region.scrollTop = 120;

    rerender(list({ messages, loadingOlder: false }));

    expect(region.scrollTop).toBe(120);
  });
});

describe("a permalink", () => {
  it("does not throw on an id that is not in the conversation", () => {
    // Belt and braces: the id is validated at the route boundary, and the
    // lookup here matches by attribute rather than building a selector, so
    // even a crafted `"]` cannot become a SyntaxError.
    expect(() => {
      render(
        list({
          messages: [message("50", "2026-08-21T09:00:00.000Z")],
          loadingOlder: false,
          focusMessageId: '"] , script',
        }),
      );
    }).not.toThrow();

    expect(screen.getByRole("log")).toBeInTheDocument();
  });

  it("scrolls to its message once, not on every later arrival", () => {
    const target = message("50", "2026-08-21T09:00:00.000Z");
    const scrollIntoView = vi.spyOn(Element.prototype, "scrollIntoView");

    const { rerender } = render(
      list({ messages: [target], loadingOlder: false, focusMessageId: target.id }),
    );
    expect(scrollIntoView).toHaveBeenCalledTimes(1);

    // Somebody else says something; the reader must stay where they landed.
    rerender(
      list({
        messages: [target, message("51", "2026-08-21T09:30:00.000Z")],
        loadingOlder: false,
        focusMessageId: target.id,
      }),
    );
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
  });
});

describe("the delete confirmation", () => {
  it("is not announced as a message arrival", () => {
    render(list({ messages: [message("50", "2026-08-21T09:00:00.000Z")], loadingOlder: false }));

    // The dialog lives outside the log region, so opening it is not an
    // arrival. Nothing is open here; what matters is the region's contents.
    expect(
      screen.getByRole("log").querySelector(".hm-confirm-layer"),
    ).toBeNull();
    expect(screen.getByRole("log")).toHaveAttribute(
      "aria-label",
      en.chat.messages.listLabel.replace("{{channel}}", "⁦#general⁩"),
    );
  });
});
