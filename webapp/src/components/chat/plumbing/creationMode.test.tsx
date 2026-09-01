import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";

// Initialises i18next; a component rendered on its own does not, and an
// uninitialised t() returns the key.
import "../../../i18n";
import en from "../../../locales/en/common.json";
import { CHAT_USERS } from "../../../mocks/chat";
import { server } from "../../../mocks/node";
import { CreateChannelDialog } from "./CreateChannelDialog";
import { PeoplePicker } from "./PeoplePicker";

/**
 * The creation surfaces under both organisation modes.
 *
 * The e2ee flag stopped being a choice: the mode decides what a conversation
 * is born as, the surfaces state that outcome instead of offering a control,
 * and they assert the value they displayed so a stale view of the mode is
 * refused by name rather than silently creating the opposite (ADR 011
 * decision 1). Compliance is not selectable through the API yet, so its branch
 * is reached the way the server's is — by setting the mode directly.
 */

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
});

afterAll(() => {
  server.close();
});

describe("creating a channel", () => {
  it("under strict, promises encryption and asserts it in the request", async () => {
    const user = userEvent.setup({ delay: null });
    const onCreate = vi.fn<(slug: string, kind: string, e2ee: boolean) => Promise<string | null>>(
      () => Promise.resolve(null),
    );
    render(<CreateChannelDialog mode="strict" onCreate={onCreate} onClose={() => undefined} />);

    expect(screen.getByText(en.chat.createChannel.e2eeByMode.strict)).toBeInTheDocument();
    // What an encrypted channel gives up, stated where the promise is made.
    expect(screen.getByText(en.chat.createChannel.e2eeNote)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).toBeNull();

    await user.type(screen.getByLabelText(en.chat.createChannel.nameLabel), "rollouts");
    await user.click(screen.getByRole("button", { name: en.chat.createChannel.submit }));

    // Sent, not omitted: the value on screen is what the request asserts.
    expect(onCreate).toHaveBeenCalledWith("rollouts", "public", true);
  });

  it("under compliance, says so plainly and asserts the other value", async () => {
    const user = userEvent.setup({ delay: null });
    const onCreate = vi.fn<(slug: string, kind: string, e2ee: boolean) => Promise<string | null>>(
      () => Promise.resolve(null),
    );
    render(<CreateChannelDialog mode="compliance" onCreate={onCreate} onClose={() => undefined} />);

    expect(screen.getByText(en.chat.createChannel.e2eeByMode.compliance)).toBeInTheDocument();
    // The encrypted-channel trade-offs are not this channel's, so they are not
    // shown; nothing here is being given up.
    expect(screen.queryByText(en.chat.createChannel.e2eeNote)).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();

    await user.type(screen.getByLabelText(en.chat.createChannel.nameLabel), "rollouts");
    await user.click(screen.getByRole("button", { name: en.chat.createChannel.submit }));

    expect(onCreate).toHaveBeenCalledWith("rollouts", "public", false);
  });

  it("says what the server actually refused when the mode has moved under it", async () => {
    const user = userEvent.setup({ delay: null });
    const onClose = vi.fn();
    render(
      <CreateChannelDialog
        mode="compliance"
        onCreate={() => Promise.resolve("e2ee_required_by_org")}
        onClose={onClose}
      />,
    );

    await user.type(screen.getByLabelText(en.chat.createChannel.nameLabel), "rollouts");
    await user.click(screen.getByRole("button", { name: en.chat.createChannel.submit }));

    // Named, not swallowed into "the channel could not be created": this is the
    // one failure the person can act on.
    expect(await screen.findByRole("alert")).toHaveTextContent(en.chat.e2eeByOrg.requiredError);
    expect(screen.queryByText(en.chat.createChannel.failed)).toBeNull();
    // And nothing was created, so the dialog stays where it is.
    expect(onClose).not.toHaveBeenCalled();
  });

  it("keeps its own message for a failure the mode does not explain", async () => {
    const user = userEvent.setup({ delay: null });
    render(
      <CreateChannelDialog
        mode="strict"
        onCreate={() => Promise.resolve("channel_slug_taken")}
        onClose={() => undefined}
      />,
    );

    await user.type(screen.getByLabelText(en.chat.createChannel.nameLabel), "rollouts");
    await user.click(screen.getByRole("button", { name: en.chat.createChannel.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.chat.createChannel.failed);
  });
});

describe("opening a direct message", () => {
  function renderPicker(
    props: Partial<React.ComponentProps<typeof PeoplePicker>> = {},
  ) {
    render(
      <PeoplePicker
        title={en.chat.sidebar.newDirectMessage}
        actionLabel={en.chat.people.message}
        encryptionMode="strict"
        onPick={() => Promise.resolve(null)}
        onClose={() => undefined}
        {...props}
      />,
    );
  }

  async function pickParisa(user: ReturnType<typeof userEvent.setup>) {
    const picker = screen.getByRole("dialog", { name: en.chat.sidebar.newDirectMessage });
    await user.click(
      await within(picker).findByRole("button", {
        name: `${en.chat.people.message}: ${CHAT_USERS.parisa.display_name}`,
      }),
    );
  }

  it("under strict, states what the conversation will be and that reopening ignores it", () => {
    renderPicker();

    expect(screen.getByText(en.chat.people.e2eeByMode.strict)).toBeInTheDocument();
    // Get-or-create is idempotent, and the reader cannot tell from the list
    // which people they already have a conversation with.
    expect(screen.getByText(en.chat.people.reopenNote)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("under compliance, states the other outcome", () => {
    renderPicker({ encryptionMode: "compliance" });

    expect(screen.getByText(en.chat.people.e2eeByMode.compliance)).toBeInTheDocument();
    expect(screen.queryByText(en.chat.people.e2eeByMode.strict)).toBeNull();
  });

  it("says nothing about encryption when it is not the creation moment", () => {
    // The invite picker joins a conversation that exists and has already
    // decided, so there is nothing for the mode to say here.
    renderPicker({ encryptionMode: undefined, title: en.chat.empty.invite });

    expect(screen.queryByText(en.chat.people.e2eeByMode.strict)).toBeNull();
    expect(screen.queryByText(en.chat.people.reopenNote)).toBeNull();
  });

  it("surfaces the mode refusal rather than a generic failure", async () => {
    const user = userEvent.setup({ delay: null });
    renderPicker({ onPick: () => Promise.resolve("e2ee_forbidden_by_org") });

    await pickParisa(user);

    expect(await screen.findByRole("alert")).toHaveTextContent(en.chat.e2eeByOrg.forbiddenError);
    expect(screen.queryByText(en.chat.people.failed)).toBeNull();
  });

  it("keeps its own message for anything else", async () => {
    const user = userEvent.setup({ delay: null });
    renderPicker({ onPick: () => Promise.resolve("unexpected") });

    await pickParisa(user);

    expect(await screen.findByRole("alert")).toHaveTextContent(en.chat.people.failed);
  });
});
