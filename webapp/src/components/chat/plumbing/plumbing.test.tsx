import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import en from "../../../locales/en/common.json";
import { CHAT_CHANNELS, CHAT_USERS, mockChannel } from "../../../mocks/chat";
import { FIXTURE_ADMIN, resetMockAuth } from "../../../mocks/handlers";
import { server } from "../../../mocks/node";
import { ChatApp } from "../../../screens/ChatApp";

/**
 * The undesigned surfaces (docs/design/STATUS.md): create-channel, the invite
 * and DM pickers, the mention picker and the account panel. They carry no
 * visual design yet, so these tests only assert that the plumbing behind them
 * does the contract-correct thing.
 */

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
  resetMockAuth();
});

afterAll(() => {
  server.close();
});

function renderChat(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ChatApp
        currentUser={FIXTURE_ADMIN}
        onLogout={() => undefined}
        realtime={{ retryDelayMs: () => 5 }}
      />
    </MemoryRouter>,
  );
}

async function openDeploys() {
  const user = userEvent.setup({ delay: null });
  renderChat(`/c/${CHAT_CHANNELS.deploys}`);
  await screen.findByText("Rolling to canary in ten minutes.");
  return user;
}

describe("create channel", () => {
  it("creates a channel and opens it, without promising public means browsable", async () => {
    const user = await openDeploys();

    await user.click(screen.getByRole("button", { name: en.chat.sidebar.createChannel }));
    const dialog = await screen.findByRole("dialog", { name: en.chat.createChannel.title });

    // Honest copy: membership is the only visibility rule in Phase 1.2.
    expect(within(dialog).getByText(en.chat.createChannel.visibilityNote)).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText(en.chat.createChannel.nameLabel), "rollouts");
    await user.click(within(dialog).getByRole("button", { name: en.chat.createChannel.submit }));

    // Creating closes the dialog, the router lands in the new channel, and its
    // (empty) history loads — three chained awaits, which outrun findBy's default
    // 1s budget on a loaded runner (observed flake, so the budget is explicit).
    // Widened again after 5 s was still outrun with the whole suite in parallel;
    // a genuinely broken create still fails on testTimeout.
    expect(
      await screen.findByText(en.chat.empty.onlyYou, undefined, { timeout: 10_000 }),
    ).toBeInTheDocument();
    // Landing in the channel also means the dialog is gone.
    expect(
      screen.queryByRole("dialog", { name: en.chat.createChannel.title }),
    ).not.toBeInTheDocument();
  });
});

describe("invite people", () => {
  it("invites somebody from the directory into the open channel", async () => {
    const user = await openDeploys();
    const before = mockChannel(CHAT_CHANNELS.deploys)?.member_count ?? 0;

    await user.click(screen.getByRole("button", { name: en.chat.header.channelActions }));
    await user.click(await screen.findByRole("button", { name: en.chat.empty.invite }));

    const picker = await screen.findByRole("dialog", { name: en.chat.empty.invite });
    await user.click(
      await within(picker).findByRole("button", {
        name: `${en.chat.people.invite}: ${CHAT_USERS.parisa.display_name}`,
      }),
    );

    await waitFor(() => {
      expect(mockChannel(CHAT_CHANNELS.deploys)?.member_count).toBe(before + 1);
    });
  });
});

describe("the mention picker", () => {
  it("inserts the wire token, never the display name", async () => {
    const user = await openDeploys();

    await user.click(screen.getByRole("button", { name: en.chat.composer.mention }));
    await user.click(await screen.findByRole("button", { name: CHAT_USERS.nasrin.display_name }));

    const field = screen.getByRole("textbox", {
      name: new RegExp(en.chat.composer.placeholder.replace("{{target}}", "")),
    });
    expect(field).toHaveValue(`<@${CHAT_USERS.nasrin.id}>`);
  });
});

describe("the channel menu", () => {
  it("sets the topic through the contract's PATCH", async () => {
    const user = await openDeploys();

    await user.click(screen.getByRole("button", { name: en.chat.header.channelActions }));
    const menu = await screen.findByRole("dialog", { name: en.chat.header.channelActions });
    const topic = within(menu).getByLabelText(en.chat.channelMenu.topicLabel);
    await user.clear(topic);
    await user.type(topic, "Rollouts and rollbacks");
    await user.click(within(menu).getByRole("button", { name: en.chat.channelMenu.saveTopic }));

    await waitFor(() => {
      expect(mockChannel(CHAT_CHANNELS.deploys)?.topic).toBe("Rollouts and rollbacks");
    });
    expect(await screen.findByText("Rollouts and rollbacks")).toBeInTheDocument();
  });
});
