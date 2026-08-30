import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import i18n from "../../../i18n";
import en from "../../../locales/en/common.json";
import fa from "../../../locales/fa/common.json";
import { initialBackupState } from "../../../mls/types";
import type { MlsBackupState, OpenBackupOutcome } from "../../../mls/types";
import { BackupIndicator, BackupSurfaces } from "./BackupPlumbing";

/**
 * The four undesigned recovery surfaces. No artboard exists yet, so these
 * assert the plumbing and the handful of things ADR 010 makes non-negotiable
 * whatever the eventual design does: the key is shown once and only once, the
 * offer is an offer, the sealed date is put to the person before anything
 * lands, and the no-key path neither lies nor points at support.
 */

const copy = en.chat.e2ee.backup;

afterEach(async () => {
  await i18n.changeLanguage("en");
});

function renderSurfaces(
  backup: Partial<MlsBackupState> = {},
  overrides: Partial<Parameters<typeof BackupSurfaces>[0]> = {},
) {
  const props = {
    backup: { ...initialBackupState, status: "offer" as const, ...backup },
    deviceReady: true,
    onEnable: vi.fn(() => Promise.resolve("0001-0002-0003")),
    onDecline: vi.fn(),
    onOpen: vi.fn(() =>
      Promise.resolve({ status: "refused", reason: "noBackup" } as OpenBackupOutcome),
    ),
    onApply: vi.fn(() => Promise.resolve(true)),
    onDiscard: vi.fn(),
    ...overrides,
  };
  const view = render(<BackupSurfaces {...props} />);
  return { ...props, view };
}

describe("the enrolment offer", () => {
  it("stays out of the way until encryption is actually running", () => {
    renderSurfaces({}, { deviceReady: false });
    expect(screen.queryByText(copy.offerTitle)).toBeNull();
  });

  it("says what it saves, what it does not, and what losing the key costs", () => {
    renderSurfaces();
    expect(screen.getByText(copy.offerTitle)).toBeInTheDocument();
    // The sentence people most need: this is not a backup of your messages.
    expect(screen.getByText(copy.offerNotMessages)).toBeInTheDocument();
    expect(screen.getByText(copy.offerShownOnce)).toBeInTheDocument();
    expect(screen.getByText(copy.offerLosingIt)).toBeInTheDocument();
  });

  it("records a decline and stops asking for the rest of the session", async () => {
    const { onDecline } = renderSurfaces();
    await userEvent.click(screen.getByRole("button", { name: copy.offerNotNow }));

    expect(onDecline).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(copy.offerTitle)).toBeNull();
  });
});

describe("the show-once ceremony", () => {
  it("shows the key, direction-pinned, and never shows it again", async () => {
    renderSurfaces({}, { onEnable: vi.fn(() => Promise.resolve("ABCD-EFGH-JKMN")) });
    await userEvent.click(screen.getByRole("button", { name: copy.offerSetUp }));

    const key = await screen.findByLabelText(copy.keyLabel);
    // Typed back character for character on another device, so it has to read
    // in the same order in both locales.
    expect(key).toHaveAttribute("dir", "ltr");
    expect(key.textContent?.replace(/[⁦-⁩]/gu, "")).toBe("ABCD-EFGH-JKMN");
    expect(screen.getByText(copy.ceremonyOnce)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: copy.ceremonyDone }));
    // Gone from the DOM and gone from this component's state: there is no
    // "show it again", because a copy that can be shown again is a copy.
    expect(screen.queryByLabelText(copy.keyLabel)).toBeNull();
    expect(screen.queryByText("ABCD-EFGH-JKMN")).toBeNull();
  });

  it("says so when the browser could not keep the key", async () => {
    renderSurfaces({}, { onEnable: vi.fn(() => Promise.resolve(null)) });
    await userEvent.click(screen.getByRole("button", { name: copy.offerSetUp }));

    expect(await screen.findByRole("alert")).toHaveTextContent(copy.setUpFailed);
    // And it did not pretend a ceremony happened.
    expect(screen.queryByLabelText(copy.keyLabel)).toBeNull();
  });
});

describe("the restore screen", () => {
  async function openRestore(overrides: Partial<Parameters<typeof BackupSurfaces>[0]> = {}) {
    const props = renderSurfaces({}, overrides);
    await userEvent.click(screen.getByRole("button", { name: copy.offerHaveKey }));
    return props;
  }

  it("puts the sealed date to the person before anything lands", async () => {
    const { onApply, view } = await openRestore({
      onOpen: vi.fn(() =>
        Promise.resolve({
          status: "opened",
          restore: {
            createdAt: "2026-03-03T09:15:00.000Z",
            serverUpdatedAt: "2026-03-03T09:15:01.000Z",
            records: 2,
          },
        } as OpenBackupOutcome),
      ),
    });

    await userEvent.type(screen.getByLabelText(copy.keyLabel), "0001-0002");
    await userEvent.click(screen.getByRole("button", { name: copy.restoreSubmit }));

    // The screen only shows the date once the service publishes the pending
    // restore, which is what the shell re-renders with.
    view.rerender(
      <BackupSurfaces
        backup={{
          ...initialBackupState,
          status: "offer",
          pending: {
            createdAt: "2026-03-03T09:15:00.000Z",
            serverUpdatedAt: "2026-03-03T09:15:01.000Z",
            records: 2,
          },
        }}
        deviceReady
        onEnable={vi.fn(() => Promise.resolve(null))}
        onDecline={vi.fn()}
        onOpen={vi.fn(() =>
          Promise.resolve({ status: "refused", reason: "failed" } as OpenBackupOutcome),
        )}
        onApply={onApply}
        onDiscard={vi.fn()}
      />,
    );

    // The whole date, year included: "3 March" would be equally true of a
    // backup from last year, which is the confusion this confirmation exists
    // to prevent.
    expect(screen.getByText(/March 3, 2026/u)).toBeInTheDocument();
    expect(screen.getByText(copy.restoreConfirmDate)).toBeInTheDocument();
    // Nothing has been applied by merely opening it.
    expect(onApply).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: copy.restoreConfirm }));
    expect(onApply).toHaveBeenCalledTimes(1);
  });

  it("renders each refusal as its own sentence", async () => {
    const reasons = [
      ["badKey", copy.failure.badKey],
      ["noBackup", copy.failure.noBackup],
      ["rolledBack", copy.failure.rolledBack],
      ["notEmpty", copy.failure.notEmpty],
    ] as const;

    for (const [reason, sentence] of reasons) {
      const { view } = await openRestore({
        onOpen: vi.fn(() => Promise.resolve({ status: "refused", reason } as OpenBackupOutcome)),
      });
      await userEvent.type(screen.getByLabelText(copy.keyLabel), "0001-0002");
      await userEvent.click(screen.getByRole("button", { name: copy.restoreSubmit }));

      expect(await screen.findByRole("alert")).toHaveTextContent(sentence);
      view.unmount();
    }
  });

  it("does not offer to save the recovery key to a password manager", async () => {
    await openRestore();
    const field = screen.getByLabelText(copy.keyLabel);
    // A manager offering to save this would be storing the one thing the
    // whole design says is stored nowhere.
    expect(field).toHaveAttribute("type", "text");
    expect(field).toHaveAttribute("autocomplete", "off");
  });
});

describe("the no-key failure path", () => {
  it("states the loss plainly and does not send anybody to support", async () => {
    renderSurfaces();
    await userEvent.click(screen.getByRole("button", { name: copy.offerHaveKey }));
    await userEvent.click(screen.getByRole("button", { name: copy.restoreNoKey }));

    expect(screen.getByText(copy.noKeyCannotOpen)).toBeInTheDocument();
    expect(screen.getByText(copy.noKeyNobodyHasIt)).toBeInTheDocument();
    // The bounded loss, said out loud: the account and the conversations
    // continue, and only the trust decisions are gone.
    expect(screen.getByText(copy.noKeyWhatContinues)).toBeInTheDocument();
    expect(screen.getByText(copy.noKeyByDesign)).toBeInTheDocument();

    // Nothing on this screen may read as "someone can get it back for you".
    const text = screen.getByRole("region", { hidden: true }).textContent ?? "";
    for (const forbidden of ["contact support", "recover it for you", "reset"]) {
      expect(text.toLowerCase()).not.toContain(forbidden);
    }
  });

  it("says the same thing in Persian", async () => {
    await i18n.changeLanguage("fa");
    renderSurfaces();
    await userEvent.click(
      screen.getByRole("button", { name: fa.chat.e2ee.backup.offerHaveKey }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: fa.chat.e2ee.backup.restoreNoKey }),
    );

    expect(screen.getByText(fa.chat.e2ee.backup.noKeyCannotOpen)).toBeInTheDocument();
    expect(screen.getByText(fa.chat.e2ee.backup.noKeyNobodyHasIt)).toBeInTheDocument();
  });
});

describe("the quiet indicator", () => {
  it("says nothing while the backup is working", () => {
    render(<BackupIndicator backup={{ ...initialBackupState, status: "on" }} />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("re-surfaces a decline passively and reports a stuck upload", () => {
    const { rerender } = render(
      <BackupIndicator backup={{ ...initialBackupState, status: "declined" }} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(copy.declinedNote);

    rerender(
      <BackupIndicator backup={{ ...initialBackupState, status: "on", writeFailed: true }} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(copy.writeFailed);
  });
});
