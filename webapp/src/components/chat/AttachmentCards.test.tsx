import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import i18n from "../../i18n";
import type { Message, UserSummary } from "../../chat/types";
import en from "../../locales/en/common.json";
import { FIXTURE_FILE, FIXTURE_IMAGE, FIXTURE_LINK_PREVIEW } from "../../mocks/chat";
import { AttachmentCards } from "./AttachmentCards";

/**
 * The file, image and link-preview cards from chat-components, against fixed
 * fixtures. What a real upload produces is asserted end to end instead
 * (webapp/e2e/specs/chat-files.e2e.ts); these pin the rendering.
 */

const AUTHOR: UserSummary = { id: "u-other", username: "omid", display_name: "Omid" };

function message(overrides: Partial<Message>): Message {
  return {
    id: "m1",
    channel_id: "c1",
    author: AUTHOR,
    client_msg_id: "client-m1",
    content: "Checklist for the rollout",
    created_at: "2026-08-21T09:20:00.000Z",
    attachments: [],
    ...overrides,
  };
}

describe("the file card", () => {
  it("names the file, its type and its size, with a labelled download", () => {
    render(<AttachmentCards message={message({ attachments: [FIXTURE_FILE] })} />);

    expect(screen.getByText(FIXTURE_FILE.filename)).toBeInTheDocument();
    expect(screen.getByText("PDF · 248 KB")).toBeInTheDocument();

    const download = screen.getByRole("link", {
      name: en.chat.messages.download.replace("{{filename}}", `⁦${FIXTURE_FILE.filename}⁩`),
    });
    expect(download).toHaveAttribute("href", FIXTURE_FILE.url);
    expect(download).toHaveAttribute("download", FIXTURE_FILE.filename);
  });

  it("keeps the filename an LTR run so it reads correctly in Persian", () => {
    render(<AttachmentCards message={message({ attachments: [FIXTURE_FILE] })} />);

    expect(screen.getByText(FIXTURE_FILE.filename)).toHaveAttribute("dir", "ltr");
  });
});

describe("the image card", () => {
  it("draws the thumbnail and the dimensions line", () => {
    const thumbnailed = { ...FIXTURE_IMAGE, thumbnail_url: "/files/latency-canary-thumb.png" };
    render(<AttachmentCards message={message({ attachments: [thumbnailed] })} />);

    const thumbnail = screen.getByRole("img", { name: FIXTURE_IMAGE.filename });
    expect(thumbnail).toHaveAttribute("src", thumbnailed.thumbnail_url);
    expect(screen.getByText(/PNG · 1\.2 MB · 1,?600 × 900/)).toBeInTheDocument();
  });

  it("falls back to a glyph when no thumbnail was produced", () => {
    // The contract types thumbnail_url as nullable, and the fixture carries
    // none — the card still has to name the file and its size.
    render(<AttachmentCards message={message({ attachments: [FIXTURE_IMAGE] })} />);

    expect(screen.queryByRole("img", { name: FIXTURE_IMAGE.filename })).not.toBeInTheDocument();
    expect(screen.getByText(FIXTURE_IMAGE.filename)).toBeInTheDocument();
    expect(screen.getByText(/PNG · 1\.2 MB/)).toBeInTheDocument();
  });
});

describe("the link preview card", () => {
  it("shows the title, description and derived host — never the raw URL", () => {
    render(<AttachmentCards message={message({ link_preview: FIXTURE_LINK_PREVIEW })} />);

    const card = screen.getByRole("link", { name: /Canary latency writeup/ });
    expect(card).toHaveAttribute("href", FIXTURE_LINK_PREVIEW.url);
    // Untrusted third-party destination.
    expect(card).toHaveAttribute("rel", "noopener noreferrer nofollow ugc");
    expect(screen.getByText("status.example.test")).toBeInTheDocument();
    expect(screen.queryByText(FIXTURE_LINK_PREVIEW.url)).not.toBeInTheDocument();
  });

  it("omits the title and description when the preview carries neither", () => {
    render(<AttachmentCards message={message({ link_preview: { url: "https://example.test/x" } })} />);

    expect(screen.getByText("example.test")).toBeInTheDocument();
  });
});

describe("all three together", () => {
  it("renders every card a message can carry", async () => {
    render(
      <AttachmentCards
        message={message({
          attachments: [FIXTURE_FILE, FIXTURE_IMAGE],
          link_preview: FIXTURE_LINK_PREVIEW,
        })}
      />,
    );

    expect(screen.getByText(FIXTURE_FILE.filename)).toBeInTheDocument();
    expect(screen.getByText(FIXTURE_IMAGE.filename)).toBeInTheDocument();
    expect(screen.getByText("status.example.test")).toBeInTheDocument();

    // ...and the same in Persian, where the size follows the locale's digits
    // while the filename stays an isolated LTR run.
    await i18n.changeLanguage("fa");
    try {
      expect(screen.getByText(FIXTURE_FILE.filename)).toHaveAttribute("dir", "ltr");
    } finally {
      await i18n.changeLanguage("en");
    }
  });
});
