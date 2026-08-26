import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { formatTime } from "../../chat/format";
import { isolateAuto } from "../../i18n/bidi";
import i18n from "../../i18n";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import { CHAT_CHANNELS, CHAT_USERS, mockChannel, mockMessages } from "../../mocks/chat";
import { FIXTURE_ADMIN, resetMockAuth } from "../../mocks/handlers";
import { server } from "../../mocks/node";
import { dropRealtimeSockets, emitRealtime } from "../../mocks/ws";
import { ChatApp } from "../../screens/ChatApp";
import { scrollIntoIntersection } from "../../test/intersectionObserver";

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(async () => {
  server.resetHandlers();
  resetMockAuth();
  await i18n.changeLanguage("en");
});

afterAll(() => {
  server.close();
});

/** Fast backoff so the reconnect path does not spend real seconds waiting. */
const FAST_RETRY = { retryDelayMs: () => 5 };

/**
 * A locale template with `{{seconds}}` widened to any number.
 *
 * The countdown ticks on a real one-second interval, so pinning the exact
 * number would make the assertion a race against the wall clock. What is being
 * asserted is that the schedule is shown at all, which does not tick.
 */
function withAnyCount(template: string, placeholder: string): RegExp {
  const escape = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const [head = "", tail = ""] = template.split(placeholder);
  return new RegExp(`${escape(head)}\\d+${escape(tail)}`);
}

/**
 * `#general` carries sixty messages, and reaching one means a channel-list
 * fetch, a redirect, a history fetch and sixty markdown bodies rendered — 900 ms
 * on an idle runner, which findBy's 1 s default has no headroom over, and
 * several seconds once a dozen jsdom workers are competing for memory.
 *
 * The budget is a wait, not an assertion: what is asserted is unchanged, and a
 * genuinely broken render still fails, on `testTimeout` (15 s) instead. A
 * budget shorter than the work only buys flakes.
 */
const LONG_HISTORY = { timeout: 10_000 };

/**
 * A channel id that is in no list this account can see — a stale permalink, or
 * an invitation that was revoked.
 *
 * Every channel-scoped path answers 404 to a non-member, exactly as it answers
 * 404 to an id that never existed. The two are indistinguishable on purpose,
 * and the shell must not distinguish them either.
 */
const UNSEEN_CHANNEL = "00000000-0000-4000-8000-0000000000ff";

function renderChat(path = "/", realtime: { retryDelayMs: () => number } = FAST_RETRY) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ChatApp
        currentUser={FIXTURE_ADMIN}
        onLogout={() => undefined}
        realtime={realtime}
      />
    </MemoryRouter>,
  );
}

function sidebar(label = en.chat.sidebar.label): HTMLElement {
  return screen.getByRole("navigation", { name: label });
}

function conversation(): HTMLElement {
  return screen.getByRole("log");
}

function composer(): HTMLTextAreaElement {
  return screen.getByRole("textbox", {
    name: new RegExp(en.chat.composer.placeholder.replace("{{target}}", "")),
  });
}

/** The `.hm-msg` wrapper of the message carrying this text. */
function messageWith(text: string | RegExp): HTMLElement {
  const node = within(conversation()).getByText(text);
  const element = node.closest<HTMLElement>("[data-message-id]");
  if (element === null) {
    throw new Error("that text is not inside a message");
  }
  return element;
}

/**
 * The composer's hidden file input. The paperclip opens it in a browser;
 * jsdom has no picker, so the test hands the file to the input directly.
 */
function filePicker(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]');
  if (input === null) {
    throw new Error("the composer has no file input");
  }
  return input;
}

async function openDeploys(
  realtime: { retryDelayMs: () => number } = FAST_RETRY,
): Promise<UserEvent> {
  const user = userEvent.setup({ delay: null });
  renderChat(`/c/${CHAT_CHANNELS.deploys}`, realtime);
  await screen.findByText("Rolling to canary in ten minutes.");
  return user;
}

describe("sidebar", () => {
  it("lists channels and direct messages with their unread and mention treatment", async () => {
    renderChat(`/c/${CHAT_CHANNELS.deploys}`);
    const nav = await screen.findByRole("navigation", { name: en.chat.sidebar.label });

    const mention = within(nav).getByRole("link", { name: /design-review/ });
    expect(mention).toHaveAttribute("data-unread", "true");
    // A mention is a filled badge carrying "@"; unread is a plain count.
    expect(within(mention).getByText("@2")).toBeInTheDocument();
    expect(within(mention).getByText(en.chat.sidebar.mentionsLabel)).toBeInTheDocument();

    const unread = within(nav).getByRole("link", { name: /leads/ });
    expect(unread).toHaveAttribute("data-unread", "true");
    expect(within(unread).getByText("4")).toBeInTheDocument();
    expect(within(unread).getByText(en.chat.sidebar.unreadLabel)).toBeInTheDocument();

    // Presence is never a bare dot: the DM row carries a text label for AT.
    const dm = within(nav).getByRole("link", { name: /Parisa Kamali/ });
    expect(within(dm).getByRole("img", { name: en.chat.presence.offline })).toBeInTheDocument();

    // The channel just opened is read, so it carries no badge at all.
    await waitFor(() => {
      expect(
        within(within(nav).getByRole("link", { name: /deploys/ })).queryByText("2"),
      ).not.toBeInTheDocument();
    });
  });

  it("opens the first conversation when the app is entered without one", async () => {
    renderChat("/");
    expect(
      await screen.findByText("Backlog note 60", undefined, LONG_HISTORY),
    ).toBeInTheDocument();
  });
});

describe("the conversation list", () => {
  it("invites the user to create a channel only when the list is really empty", async () => {
    server.use(http.get("/api/v1/channels", () => HttpResponse.json({ channels: [] })));
    renderChat("/");

    expect(await screen.findByText(en.chat.noConversations)).toBeInTheDocument();
  });

  it("says the list could not be loaded rather than that the account is empty", async () => {
    // A failed load and an empty account are not the same fact, and the second
    // one is an accusation: it tells someone with twenty channels that they
    // have none, and invites them to make another.
    server.use(http.get("/api/v1/channels", () => HttpResponse.error()));
    renderChat("/");

    expect(await screen.findByText(en.chat.conversationsFailed)).toBeInTheDocument();
    expect(screen.queryByText(en.chat.noConversations)).not.toBeInTheDocument();
  });

  it("says a conversation it cannot show is unavailable, not that the account is empty", async () => {
    // What a stale permalink or a revoked invitation produces. The empty-account
    // invitation was being shown here next to a sidebar listing the conversations
    // the reader does have — a plain falsehood. The replacement says the
    // conversation is not available to this reader and points at the list, which
    // commits to nothing about whether the id names anything at all.
    renderChat(`/c/${UNSEEN_CHANNEL}`);

    expect(await screen.findByText(en.chat.conversationUnavailable)).toBeInTheDocument();
    expect(screen.queryByText(en.chat.noConversations)).not.toBeInTheDocument();
    expect(screen.queryByText(en.chat.conversationsFailed)).not.toBeInTheDocument();
    // The sidebar the sentence sends the reader to really is beside it.
    expect(within(sidebar()).getByRole("link", { name: /deploys/ })).toBeInTheDocument();
  });

  it("says nothing about the list while it is still loading", async () => {
    // Held open by the test rather than by wall-clock delay, so a loaded runner
    // cannot miss the loading state.
    let releaseChannels: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      releaseChannels = resolve;
    });
    server.use(
      http.get("/api/v1/channels", async () => {
        await gate;
        return HttpResponse.json({ channels: [] });
      }),
    );

    try {
      renderChat("/");

      expect(await screen.findByText(en.common.loading)).toBeInTheDocument();
      expect(screen.queryByText(en.chat.noConversations)).not.toBeInTheDocument();
      expect(screen.queryByText(en.chat.conversationsFailed)).not.toBeInTheDocument();
    } finally {
      // Never leave the handler (and therefore the request) hanging if an
      // assertion above threw.
      releaseChannels?.();
    }
  });
});

describe("opening a channel", () => {
  it("loads history and stores the read position at the newest message", async () => {
    await openDeploys();

    const newest = mockMessages(CHAT_CHANNELS.deploys).at(-1);
    await waitFor(() => {
      expect(mockChannel(CHAT_CHANNELS.deploys)?.last_read_message_id).toBe(newest?.id);
    });
  });

  it("collapses a run from one author under a single avatar, name and time", async () => {
    await openDeploys();

    // Two consecutive messages from Nasrin, one meta row: the second message
    // is on screen but carries neither a name nor a timestamp of its own.
    expect(within(conversation()).getAllByText("Nasrin Ahmadi")).toHaveLength(1);
    expect(
      within(conversation()).getByText("Rolling to canary in ten minutes."),
    ).toBeInTheDocument();
    expect(
      within(conversation()).getAllByText(formatTime("2026-08-21T09:12:00.000Z", "en")),
    ).toHaveLength(1);
    expect(
      within(conversation()).queryByText(formatTime("2026-08-21T09:13:00.000Z", "en")),
    ).not.toBeInTheDocument();
  });

  it("renders the edited and removed markers the contract's flags imply", async () => {
    await openDeploys();

    expect(within(conversation()).getByText(en.chat.messages.edited)).toBeInTheDocument();
    expect(within(conversation()).getByText(en.chat.messages.removed)).toBeInTheDocument();
  });

  it("shows the guidance state instead of a void in an empty channel", async () => {
    renderChat(`/c/${CHAT_CHANNELS.designTokens}`);

    expect(
      await screen.findByText(new RegExp(en.chat.empty.title.replace("{{channel}}", ""))),
    ).toBeInTheDocument();
    // Honest copy: nothing here promises a browsable public channel.
    expect(screen.getByText(en.chat.empty.onlyYou)).toBeInTheDocument();
    // Both actions the contract supports on a channel are offered.
    expect(screen.getByRole("button", { name: en.chat.empty.invite })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.chat.empty.setTopic })).toBeInTheDocument();
  });

  it("names the peer in an empty direct message and offers neither refused action", async () => {
    // A DM has no slug, so the channel copy rendered as "the beginning of #",
    // and it has no topic and fixed membership, so the server answers 400 to
    // both actions the channel state offers. The closing note was false too:
    // the other person is already here, invited by nobody.
    server.use(
      http.get("/api/v1/channels/:channelId/messages", () => HttpResponse.json({ messages: [] })),
    );
    renderChat(`/c/${CHAT_CHANNELS.dmParisa}`);

    // Named by the peer, isolated first-strong exactly as MessageList isolates it.
    expect(
      await screen.findByText(
        en.chat.empty.dmTitle.replace(
          "{{name}}",
          isolateAuto(CHAT_USERS.parisa.display_name),
        ),
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(en.chat.empty.dmBody)).toBeInTheDocument();

    expect(screen.queryByRole("button", { name: en.chat.empty.invite })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.chat.empty.setTopic })).not.toBeInTheDocument();
    expect(screen.queryByText(en.chat.empty.onlyYou)).not.toBeInTheDocument();
    expect(screen.queryByText(en.chat.empty.body)).not.toBeInTheDocument();
    expect(screen.queryByText(en.chat.empty.bodyOwner)).not.toBeInTheDocument();
  });
});

describe("a permalink", () => {
  it("opens the message a search result links to", async () => {
    const target = mockMessages(CHAT_CHANNELS.deploys)[0];
    renderChat(`/c/${CHAT_CHANNELS.deploys}/m/${String(target?.id)}`);

    expect(await screen.findByText("Rolling to canary in ten minutes.")).toBeInTheDocument();
  });

  it("ignores an id that is not a uuid instead of taking the app down", async () => {
    // A crafted id used to reach a CSS attribute selector, where `"]` throws a
    // SyntaxError — and an uncaught throw here unmounts the whole React root.
    renderChat(`/c/${CHAT_CHANNELS.deploys}/m/${encodeURIComponent('x"] , *')}`);

    expect(await screen.findByText("Rolling to canary in ten minutes.")).toBeInTheDocument();
    expect(screen.queryByText(en.app.error.title)).not.toBeInTheDocument();
  });
});

describe("the composer", () => {
  it("uploads a picked file, then sends it with the message", async () => {
    const user = await openDeploys();

    const file = new File(["checklist"], "rollout.txt", { type: "text/plain" });
    await user.upload(filePicker(), file);

    // Waited on by the card's download control rather than by the filename:
    // the filename is drawn while the upload is still in flight too, and a
    // click then would find send correctly disabled and prove nothing. The
    // download link exists only on the delivered card, once the 201 is in.
    const tray = await screen.findByRole("list", { name: en.chat.composer.attachments });
    expect(await within(tray).findByRole("link")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: en.chat.composer.send }));

    // A message whose only payload is a file: the send path must not refuse
    // it for having no text. (Filename and size are not asserted — see the
    // mock's singleFilePart for why a mocked upload loses both.)
    await waitFor(() => {
      const sent = mockMessages(CHAT_CHANNELS.deploys).at(-1);
      expect(sent?.attachments).toHaveLength(1);
      expect(sent?.content).toBe("");
    });
    // The tray empties with the send; the files now live on the message.
    expect(
      screen.queryByRole("list", { name: en.chat.composer.attachments }),
    ).not.toBeInTheDocument();
  });

  it("refuses a file larger than the instance accepts, without a request", async () => {
    const user = await openDeploys();
    const before = mockMessages(CHAT_CHANNELS.deploys).length;

    const huge = new File(["x"], "huge.bin", { type: "application/octet-stream" });
    // File.size is derived from the parts; redefining it beats allocating
    // 26 MiB inside a unit test to prove a comparison.
    Object.defineProperty(huge, "size", { value: 26 * 1024 * 1024 });
    await user.upload(filePicker(), huge);

    expect(
      await screen.findByText(
        en.chat.composer.uploadTooLarge.replace("{{limit}}", "25 MB"),
      ),
    ).toBeInTheDocument();
    // Send stays shut until the failed file is removed — it must never be
    // dropped silently from the message.
    expect(screen.getByRole("button", { name: en.chat.composer.send })).toBeDisabled();
    expect(mockMessages(CHAT_CHANNELS.deploys)).toHaveLength(before);
  });
});

describe("the unread divider", () => {
  it("is placed at the read position and holds while new messages arrive", async () => {
    const user = await openDeploys();

    const divider = await screen.findByText(en.chat.messages.newMessages);
    const anchor = messageWith("Latency held flat through the canary — writeup here.");
    // Placed immediately before the first message the caller has not read.
    expect(divider.compareDocumentPosition(anchor) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    await user.type(composer(), "Following along.{Enter}");
    await screen.findByText("Following along.");

    // Still there, and still in the same place.
    expect(screen.getByText(en.chat.messages.newMessages)).toBe(divider);
    expect(divider.compareDocumentPosition(anchor) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe("sending", () => {
  it("leaves the channel in the order the messages were composed", async () => {
    const user = await openDeploys();

    // The server stamps created_at as each POST arrives, so the order they
    // leave in IS the order history comes back in. Firing them concurrently
    // was a real bug: three messages typed quickly raced, and the author saw
    // their own words rearranged after a reload — optimistic rendering hid it
    // until then.
    const arrived: string[] = [];
    let releaseFirst: () => void = () => undefined;
    const firstHeld = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });

    server.use(
      http.post("/api/v1/channels/:channelId/messages", async ({ request }) => {
        const body = (await request.json()) as { content: string; client_msg_id: string };
        arrived.push(body.content);
        // Hold the first request open. Anything that arrives while it is held
        // proves the sends are not waiting for each other.
        if (arrived.length === 1) {
          await firstHeld;
        }
        return HttpResponse.json(
          {
            id: crypto.randomUUID(),
            channel_id: CHAT_CHANNELS.deploys,
            author: FIXTURE_ADMIN,
            client_msg_id: body.client_msg_id,
            content: body.content,
            // Required by the contract, and the server sends it empty until
            // the Phase 1.3 upload pipeline exists. Omitting it here made the
            // mock non-conforming and crashed AttachmentCards on an unguarded
            // map — a test defect that vitest reported as an unhandled error
            // while still passing the assertion, which is how it reached CI.
            attachments: [],
            created_at: new Date().toISOString(),
          },
          { status: 201 },
        );
      }),
    );

    await user.type(composer(), "First.{Enter}");
    await user.type(composer(), "Second.{Enter}");
    await user.type(composer(), "Third.{Enter}");

    // All three are on screen optimistically, and only the first has been
    // sent — the other two are behind it rather than racing it.
    expect(await screen.findByText("Third.")).toBeInTheDocument();
    expect(arrived).toEqual(["First."]);

    releaseFirst();
    await waitFor(() => {
      expect(arrived).toEqual(["First.", "Second.", "Third."]);
    });
  });

  it("appends the message optimistically and reconciles it by client_msg_id", async () => {
    const user = await openDeploys();

    await user.type(composer(), "Shipping now.{Enter}");
    expect(await screen.findByText("Shipping now.")).toBeInTheDocument();

    await waitFor(() => {
      expect(
        mockMessages(CHAT_CHANNELS.deploys).some((entry) => entry.content === "Shipping now."),
      ).toBe(true);
    });
    // The optimistic bubble and the stored message are one message, not two.
    expect(within(conversation()).getAllByText("Shipping now.")).toHaveLength(1);
    expect(composer()).toHaveValue("");
  });
});

describe("message actions", () => {
  it("edits a message in place and marks it edited", async () => {
    const user = await openDeploys();

    const target = messageWith("Nice. I'll watch the error rate.");
    await user.click(within(target).getByRole("button", { name: en.chat.messages.actions.edit }));

    const field = screen.getByRole("textbox", { name: en.chat.messages.editLabel });
    await user.clear(field);
    await user.type(field, "Watching the error rate.");
    await user.click(screen.getByRole("button", { name: en.chat.messages.saveEdit }));

    await screen.findByText("Watching the error rate.");
    expect(
      within(messageWith("Watching the error rate.")).getByText(en.chat.messages.edited),
    ).toBeInTheDocument();
  });

  it("confirms before deleting and leaves a placeholder in its place", async () => {
    const user = await openDeploys();

    const target = messageWith("Nice. I'll watch the error rate.");
    const messageId = target.dataset.messageId;
    await user.click(within(target).getByRole("button", { name: en.chat.messages.actions.delete }));

    const dialog = await screen.findByRole("dialog", {
      name: en.chat.messages.deleteConfirm.title,
    });
    await user.click(
      within(dialog).getByRole("button", { name: en.chat.messages.deleteConfirm.confirm }),
    );

    await waitFor(() => {
      expect(screen.queryByText("Nice. I'll watch the error rate.")).not.toBeInTheDocument();
    });
    // The row keeps its place rather than the conversation reshaping.
    const placeholder = conversation().querySelector<HTMLElement>(
      `[data-message-id="${String(messageId)}"]`,
    );
    expect(placeholder).not.toBeNull();
    expect(
      within(placeholder ?? conversation()).getByText(en.chat.messages.removed),
    ).toBeInTheDocument();
  });

  it("hands focus to the confirmation and gives it back on cancel", async () => {
    const user = await openDeploys();

    const target = messageWith("Nice. I'll watch the error rate.");
    const opener = within(target).getByRole("button", { name: en.chat.messages.actions.delete });
    await user.click(opener);

    const dialog = await screen.findByRole("dialog", {
      name: en.chat.messages.deleteConfirm.title,
    });
    // The dialog claims aria-modal, so focus starts inside it...
    expect(
      within(dialog).getByRole("button", { name: en.chat.messages.deleteConfirm.confirm }),
    ).toHaveFocus();

    await user.click(
      within(dialog).getByRole("button", { name: en.chat.messages.deleteConfirm.cancel }),
    );

    // ...and comes back to the control that opened it, not to the document.
    await waitFor(() => {
      expect(opener).toHaveFocus();
    });
  });
});

describe("scrollback", () => {
  it("fetches the previous page when the top of the history comes into view", async () => {
    renderChat(`/c/${CHAT_CHANNELS.general}`);
    await screen.findByText("Backlog note 60", undefined, LONG_HISTORY);
    expect(screen.queryByText("Backlog note 1")).not.toBeInTheDocument();

    // The sentinel is rendered a beat before its observer is attached.
    await waitFor(() => {
      scrollIntoIntersection(screen.getByTestId("hm-history-sentinel"));
    });

    expect(
      await screen.findByText("Backlog note 1", undefined, LONG_HISTORY),
    ).toBeInTheDocument();
    // The older page is prepended, not swapped in.
    expect(screen.getByText("Backlog note 60")).toBeInTheDocument();
  });
});

describe("the reconnect path", () => {
  it("queues a message the server did not take and flushes it once, on reconnect", async () => {
    const user = await openDeploys();
    await waitFor(() => {
      expect(composer()).toBeEnabled();
    });

    // The send never reaches the server.
    server.use(
      http.post("/api/v1/channels/:channelId/messages", () => HttpResponse.error()),
    );
    await user.type(composer(), "Pulling the release notes together now.{Enter}");
    expect(
      await screen.findByText(en.chat.messages.waitingToSend),
    ).toBeInTheDocument();

    // Let the send succeed again, then drop and restore the socket.
    server.resetHandlers();
    dropRealtimeSockets(4408);

    await waitFor(() => {
      expect(
        screen.queryByText(en.chat.messages.waitingToSend),
      ).not.toBeInTheDocument();
    });
    expect(
      within(conversation()).getAllByText("Pulling the release notes together now."),
    ).toHaveLength(1);
    expect(
      mockMessages(CHAT_CHANNELS.deploys).filter(
        (entry) => entry.content === "Pulling the release notes together now.",
      ),
    ).toHaveLength(1);
  });

  it("shows the connection banner and disables the composer with its reason", async () => {
    // A backoff long enough that the disconnected state the artboard draws
    // stays on screen to be asserted.
    await openDeploys({ retryDelayMs: () => 60_000 });
    await waitFor(() => {
      expect(composer()).toBeEnabled();
    });

    dropRealtimeSockets(4408);

    await waitFor(() => {
      expect(composer()).toBeDisabled();
    });
    // The reason always travels with the disabled state.
    expect(screen.getByText(en.chat.composer.disconnected)).toBeInTheDocument();
    // ...and the banner counts the retry down rather than hiding the schedule.
    expect(
      screen.getByText(withAnyCount(en.chat.connection.offline, "{{seconds}}")),
    ).toBeInTheDocument();
  });
});

describe("presence", () => {
  it("follows a DM peer's presence over the socket", async () => {
    renderChat(`/c/${CHAT_CHANNELS.deploys}`);
    const nav = await screen.findByRole("navigation", { name: en.chat.sidebar.label });
    const dm = within(nav).getByRole("link", { name: /Parisa Kamali/ });
    expect(within(dm).getByRole("img", { name: en.chat.presence.offline })).toBeInTheDocument();

    emitRealtime({
      type: "presence",
      chan: CHAT_CHANNELS.dmParisa,
      data: { user_id: CHAT_USERS.parisa.id, state: "online" },
    });

    expect(
      await within(dm).findByRole("img", { name: en.chat.presence.online }),
    ).toBeInTheDocument();
  });
});

describe("search", () => {
  it("returns results and jumps to one in its conversation", async () => {
    const user = await openDeploys();

    await user.type(
      screen.getByRole("searchbox", { name: en.chat.header.searchPlaceholder }),
      "canary{Enter}",
    );

    const panel = await screen.findByRole("complementary", { name: en.chat.search.label });
    expect(
      within(panel).getByText(
        en.chat.search.resultsOther.replace("{{count}}", "3").replace("{{query}}", "canary"),
      ),
    ).toBeInTheDocument();
    // The highlight is drawn from the snippet's `match` runs, never from HTML.
    expect(within(panel).getAllByText("canary").length).toBeGreaterThan(0);

    const dmResult = within(panel).getByRole("link", { name: /latency capture/ });
    await user.click(dmResult);

    expect(
      await screen.findByText("Sent you the latency capture from the canary window."),
    ).toBeInTheDocument();
  });
});

describe("the Persian mirror", () => {
  it("mirrors the shell from dir alone and renders the Persian copy", async () => {
    await i18n.changeLanguage("fa");
    renderChat(`/c/${CHAT_CHANNELS.deploys}`);

    await screen.findByRole("navigation", { name: fa.chat.sidebar.label });
    expect(document.documentElement).toHaveAttribute("dir", "rtl");
    expect(document.documentElement).toHaveAttribute("lang", "fa");

    const nav = sidebar(fa.chat.sidebar.label);
    expect(within(nav).getByText(fa.chat.sidebar.channels)).toBeInTheDocument();
    expect(within(nav).getByText(fa.chat.sidebar.directMessages)).toBeInTheDocument();
    expect(screen.getByText(fa.chat.composer.hintSend)).toBeInTheDocument();
  });

  it("says a failed conversation load in Persian", async () => {
    await i18n.changeLanguage("fa");
    server.use(http.get("/api/v1/channels", () => HttpResponse.error()));
    renderChat("/");

    expect(await screen.findByText(fa.chat.conversationsFailed)).toBeInTheDocument();
    expect(screen.queryByText(fa.chat.noConversations)).not.toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("dir", "rtl");
  });

  it("says an unavailable conversation in Persian", async () => {
    await i18n.changeLanguage("fa");
    renderChat(`/c/${UNSEEN_CHANNEL}`);

    expect(await screen.findByText(fa.chat.conversationUnavailable)).toBeInTheDocument();
    expect(screen.queryByText(fa.chat.noConversations)).not.toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("dir", "rtl");
  });

  it("names the peer of an empty direct message in Persian", async () => {
    await i18n.changeLanguage("fa");
    server.use(
      http.get("/api/v1/channels/:channelId/messages", () => HttpResponse.json({ messages: [] })),
    );
    renderChat(`/c/${CHAT_CHANNELS.dmParisa}`);

    // A Latin name inside a Persian sentence: the isolate is what keeps the
    // punctuation on the right side of it.
    expect(
      await screen.findByText(
        fa.chat.empty.dmTitle.replace(
          "{{name}}",
          isolateAuto(CHAT_USERS.parisa.display_name),
        ),
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(fa.chat.empty.dmBody)).toBeInTheDocument();
    expect(screen.queryByText(fa.chat.empty.onlyYou)).not.toBeInTheDocument();
  });
});
