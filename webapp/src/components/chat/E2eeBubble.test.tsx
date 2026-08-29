import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

// Initialises i18next so the components' own copy resolves.
import "../../i18n";
import type { Message } from "../../chat/types";
import { MessageBodyProvider } from "../../mls/MessageBodyContext";
import type { MessageBody } from "../../mls/types";
import { MessageBubble } from "./MessageBubble";

/**
 * What an encrypted message draws.
 *
 * The case that matters most is the last one: decrypted text is still text
 * somebody else wrote, so it goes through the same sanitizing markdown path as
 * a plaintext body. Decryption proves who sent a message, not that it is safe
 * to render as markup.
 */

const AUTHOR = { id: "u-other", username: "nasrin", display_name: "Nasrin" };

function encrypted(id = "m-1"): Message {
  return {
    id,
    client_msg_id: `c-${id}`,
    channel_id: "c-secret",
    author: AUTHOR,
    // Empty exactly when the envelope is present — the contract's rule.
    content: "",
    // Refused on an encrypted channel by the contract (400
    // e2ee_attachments_unsupported) until encrypted attachments land.
    attachments: [],
    created_at: "2026-08-29T09:00:00.000Z",
    mls: { epoch: 3, ciphertext: "AAAA" },
  };
}

function renderBubble(message: Message, body: MessageBody) {
  return render(
    <MessageBodyProvider resolve={() => body}>
      <MessageBubble
        message={message}
        first
        own={false}
        canModerate={false}
        channelId="c-secret"
        resolveMention={() => null}
        onEdit={() => Promise.resolve(true)}
        onDelete={() => undefined}
      />
    </MessageBodyProvider>,
  );
}

describe("an encrypted message", () => {
  it("says it cannot be decrypted rather than drawing an empty bubble", () => {
    const { container } = renderBubble(encrypted(), { kind: "undecryptable" });

    expect(screen.getByText(/cannot be decrypted/i)).toBeInTheDocument();
    // And nothing from the message body path: an empty `.hm-md` would be a
    // bubble that silently says nothing.
    expect(container.querySelector(".hm-md")).toBeNull();
  });

  it("draws a placeholder while the decryption is in flight", () => {
    renderBubble(encrypted(), { kind: "pending" });
    expect(screen.getByText(/decrypting/i)).toBeInTheDocument();
  });

  it("offers no edit action for a body this device cannot read", () => {
    renderBubble({ ...encrypted(), author: { id: "u-me", username: "me", display_name: "Me" } }, {
      kind: "undecryptable",
    });
    expect(screen.queryByLabelText(/edit/i)).toBeNull();
  });

  it("renders decrypted text through the same markdown path as plaintext", () => {
    const { container } = renderBubble(encrypted(), {
      kind: "decrypted",
      text: "**bold** and a [link](https://example.invalid/x)",
    });

    expect(container.querySelector("strong")?.textContent).toBe("bold");
    expect(screen.getByRole("link", { name: "link" })).toHaveAttribute(
      "href",
      "https://example.invalid/x",
    );
  });

  it("sanitizes decrypted content exactly as it sanitizes plaintext", () => {
    const { container } = renderBubble(encrypted(), {
      kind: "decrypted",
      text: "<img src=x onerror=alert(1)> and [click](javascript:alert(1))",
    });

    // Raw HTML never becomes markup, and a javascript: URL never reaches the
    // DOM — the two defences MessageContent documents, reached through the
    // decryption path.
    expect(container.querySelector("img")).toBeNull();
    expect(container.innerHTML).not.toContain("onerror");
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
  });
});
