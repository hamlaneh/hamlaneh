import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Initialises i18next so the components' own copy resolves.
import "../../i18n";
import type { Attachment, Message, UserSummary } from "../../chat/types";
import en from "../../locales/en/common.json";
import { sealAttachment, type AttachmentEntry } from "../../mls/attachments";
import { toBase64 } from "../../mls/bytes";
import { AttachmentCards, UnreadableAttachments } from "./AttachmentCards";

/**
 * The read half of ADR 013, against real ciphertext: the bytes below are
 * sealed by the same code the sender uses, fetched through a stubbed network
 * and opened by the card.
 *
 * Two of these are security tests rather than rendering ones. A decrypted blob
 * must never be something the reader can navigate to — the card holds no
 * `href` at all — and what a decrypted blob is TYPED as must come from the
 * bytes, never from what the sender said they were.
 */

const KEY = new Uint8Array(16).fill(3);
const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
const HTML = new TextEncoder().encode("<script>alert(document.domain)</script>");
const PDF = new TextEncoder().encode("%PDF-1.7 not really");

const AUTHOR: UserSummary = { id: "u-other", username: "omid", display_name: "Omid" };

/** The server's row: the placeholder name and the opaque type, as stored. */
function row(overrides: Partial<Attachment> = {}): Attachment {
  return {
    id: "a-1",
    filename: "encrypted",
    content_type: "application/octet-stream",
    size_bytes: 4096,
    url: "/files/a-1?sig=x",
    ...overrides,
  };
}

function entry(overrides: Partial<AttachmentEntry> = {}): AttachmentEntry {
  return {
    id: "a-1",
    key: toBase64(KEY),
    name: "q3-budget.pdf",
    type: "application/pdf",
    size: 4096,
    ...overrides,
  };
}

function message(attachments: Attachment[]): Message {
  return {
    id: "m-1",
    channel_id: "c-secret",
    author: AUTHOR,
    client_msg_id: "client-m-1",
    content: "",
    created_at: "2026-08-31T09:20:00.000Z",
    attachments,
  };
}

/** Every Blob a decrypted URL was minted from, in order. */
let minted: Blob[] = [];
let revoked: string[] = [];
/** The anchors `saveDecrypted` clicked. */
let saved: HTMLAnchorElement[] = [];

/** url -> sealed bytes, so a card fetches what the sender really uploaded. */
const wire = new Map<string, Uint8Array>();

beforeEach(() => {
  minted = [];
  revoked = [];
  saved = [];
  wire.clear();
  URL.createObjectURL = vi.fn((blob: Blob) => {
    minted.push(blob);
    return `blob:test/${String(minted.length)}`;
  });
  URL.revokeObjectURL = vi.fn((url: string) => {
    revoked.push(url);
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    saved.push(this);
  });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      const sealed = wire.get(url);
      return Promise.resolve(
        sealed === undefined
          ? new Response(null, { status: 404 })
          : new Response(sealed.slice().buffer),
      );
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function serve(url: string, variant: "original" | "thumb", plaintext: Uint8Array) {
  wire.set(url, await sealAttachment(KEY, variant, plaintext));
}

describe("an encrypted file card", () => {
  it("shows the real name, type and size, never the server's placeholder", async () => {
    await serve("/files/a-1?sig=x", "original", PDF);
    render(<AttachmentCards message={message([row()])} entries={[entry()]} />);

    expect(screen.getByText("q3-budget.pdf")).toBeInTheDocument();
    expect(screen.getByText("PDF · 4 KB")).toBeInTheDocument();
    expect(screen.queryByText("encrypted")).toBeNull();
  });

  it("holds no navigable URL at all — the save is a button", () => {
    const { container } = render(<AttachmentCards message={message([row()])} entries={[entry()]} />);

    expect(container.querySelector("a")).toBeNull();
    expect(
      screen.getByRole("button", {
        name: en.chat.messages.download.replace("{{filename}}", "⁦q3-budget.pdf⁩"),
      }),
    ).toBeInTheDocument();
  });

  it("saves the decrypted file as an opaque download", async () => {
    await serve("/files/a-1?sig=x", "original", PDF);
    render(<AttachmentCards message={message([row()])} entries={[entry()]} />);

    await userEvent.click(screen.getByRole("button", { name: /download/i }));

    await waitFor(() => {
      expect(saved).toHaveLength(1);
    });
    // The Blob's own type is what makes this a download and not a page: bytes
    // that are not one of the four proven image types are octet-stream, so
    // even opening the URL directly could not run them as script.
    expect(minted[0]?.type).toBe("application/octet-stream");
    expect(saved[0]?.download).toBe("q3-budget.pdf");
    expect(saved[0]?.getAttribute("href")).toMatch(/^blob:/);
  });

  it("sanitizes a hostile filename before it reaches the card or the save", async () => {
    await serve("/files/a-1?sig=x", "original", PDF);
    // A path, and a right-to-left override that would make this read
    // "…exe.png" on the screen while saving an executable.
    const hostile = entry({ name: "/etc/passwd/‮gnp.exe" });
    render(<AttachmentCards message={message([row()])} entries={[hostile]} />);

    expect(screen.getByText("gnp.exe")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /download/i }));
    await waitFor(() => {
      expect(saved[0]?.download).toBe("gnp.exe");
    });
  });

  it("says so when the file will not come back", async () => {
    render(<AttachmentCards message={message([row()])} entries={[entry()]} />);

    await userEvent.click(screen.getByRole("button", { name: /download/i }));
    expect(await screen.findByText(en.chat.messages.fileFailed)).toBeInTheDocument();
  });
});

describe("an encrypted image card", () => {
  const imageRow = row({ thumbnail_url: "/files/a-1/thumb?sig=x" });

  it("draws the decrypted thumbnail as an image typed from its own bytes", async () => {
    await serve("/files/a-1/thumb?sig=x", "thumb", PNG);
    render(
      <AttachmentCards
        message={message([imageRow])}
        entries={[entry({ name: "canary.png", type: "image/png", width: 1600, height: 900 })]}
      />,
    );

    const preview = await screen.findByRole("img", { name: "canary.png" });
    expect(preview.getAttribute("src")).toMatch(/^blob:/);
    expect(minted[0]?.type).toBe("image/png");
  });

  it("never renders a thumbnail whose bytes are not one of the four types", async () => {
    // The sender said image/png; the bytes are a script. The sniff decides.
    await serve("/files/a-1/thumb?sig=x", "thumb", HTML);
    render(
      <AttachmentCards
        message={message([imageRow])}
        entries={[entry({ name: "canary.png", type: "image/png" })]}
      />,
    );

    await waitFor(() => {
      expect(revoked).toHaveLength(1);
    });
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("falls back to the no-preview frame when the thumbnail will not open", async () => {
    // Sealed under the wrong variant: the AAD is what fails it, which is the
    // substitution a shared per-file key would otherwise permit.
    wire.set("/files/a-1/thumb?sig=x", await sealAttachment(KEY, "original", PNG));
    render(
      <AttachmentCards message={message([imageRow])} entries={[entry({ name: "canary.png" })]} />,
    );

    await waitFor(() => {
      expect(screen.queryByRole("img")).toBeNull();
    });
    expect(minted).toHaveLength(0);
  });
});

describe("a file this device cannot open", () => {
  it("says the message it came in cannot be read", () => {
    render(<UnreadableAttachments message={message([row(), row({ id: "a-2" })])} />);

    expect(screen.getAllByText(en.chat.messages.fileNeedsMessage)).toHaveLength(2);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("says when the message opened but named no key for it", () => {
    render(<AttachmentCards message={message([row()])} entries={[]} />);

    expect(screen.getByText(en.chat.messages.fileNoKey)).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("ignores an entry that names a file this message does not carry", () => {
    render(
      <AttachmentCards message={message([row()])} entries={[entry({ id: "a-somewhere-else" })]} />,
    );

    expect(screen.getByText(en.chat.messages.fileNoKey)).toBeInTheDocument();
    expect(screen.queryByText("q3-budget.pdf")).toBeNull();
  });
});
