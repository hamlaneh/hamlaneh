import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

// Initialises i18next so the component's own copy resolves.
import "../../i18n";
import { MessageContent } from "./MessageContent";

/**
 * Message content is untrusted markdown written by other users. These are the
 * cases that must never produce markup, a script, or a dangerous URL.
 */

const NAMES = new Map([["00000000-0000-4000-8000-0000000000a1", "Nasrin Ahmadi"]]);
const resolve = (userId: string) => NAMES.get(userId) ?? null;

function renderContent(content: string) {
  return render(<MessageContent content={content} resolveMention={resolve} />);
}

describe("markdown rendering", () => {
  it("renders the constructs the design draws", () => {
    const { container } = renderContent(
      "**Bold**, *italic*, `inline` and a [link](https://example.invalid/x).\n\n> A quoted line.\n\n- first item\n\n```\ngit tag v1.2.0\n```",
    );

    expect(container.querySelector("strong")?.textContent).toBe("Bold");
    expect(container.querySelector("em")?.textContent).toBe("italic");
    expect(container.querySelector("blockquote")?.textContent).toContain("A quoted line.");
    expect(container.querySelector("li")?.textContent).toContain("first item");
    expect(container.querySelector("pre code")?.textContent).toContain("git tag v1.2.0");

    const link = screen.getByRole("link", { name: "link" });
    expect(link).toHaveAttribute("href", "https://example.invalid/x");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  it("reads the body direction from the content itself", () => {
    const { container } = renderContent("سلام");
    expect(container.querySelector(".hm-md")).toHaveAttribute("dir", "auto");
  });
});

describe("mentions", () => {
  it("renders the wire token as the person's display name", () => {
    const { container } = renderContent("Ping <@00000000-0000-4000-8000-0000000000a1> about it");

    const mention = container.querySelector(".hm-mention");
    expect(mention?.textContent).toBe("@Nasrin Ahmadi");
    expect(mention).toHaveAttribute("data-user-id", "00000000-0000-4000-8000-0000000000a1");
    // The raw token never reaches the reader.
    expect(container.textContent).not.toContain("<@");
  });

  it("leaves a token inside a code span as literal text", () => {
    const { container } = renderContent("`<@00000000-0000-4000-8000-0000000000a1>`");

    expect(container.querySelector(".hm-mention")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe(
      "<@00000000-0000-4000-8000-0000000000a1>",
    );
  });

  it("falls back to a neutral label for somebody it cannot name", () => {
    const { container } = renderContent("<@00000000-0000-4000-8000-00000000ffff>");
    expect(container.querySelector(".hm-mention")?.textContent).toBe("@unknown");
  });
});

describe("the XSS corpus", () => {
  it("does not turn a script tag into a script element", () => {
    const { container } = renderContent('<script>alert("xss")</script>');

    expect(container.querySelector("script")).toBeNull();
    expect(container.innerHTML).not.toContain("<script");
  });

  it("refuses a javascript: link", () => {
    const { container } = renderContent("[click me](javascript:alert(1))");

    expect(container.querySelector("a")).toBeNull();
    expect(container.textContent).toContain("click me");
  });

  it("refuses a data: URL link", () => {
    const { container } = renderContent("[x](data:text/html;base64,PHNjcmlwdD4=)");
    expect(container.querySelector("a")).toBeNull();
  });

  it("never emits an event-handler attribute", () => {
    const { container } = renderContent('<img src=x onerror="alert(1)">');

    expect(container.querySelector("img")).toBeNull();
    expect(container.innerHTML).not.toContain("onerror");
  });

  it("keeps html inside a code fence literal", () => {
    const { container } = renderContent("```\n<script>alert(1)</script>\n```");

    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("pre code")?.textContent).toContain("<script>alert(1)</script>");
  });

  it("keeps an inline code span with html literal", () => {
    const { container } = renderContent("`<img onerror=alert(1)>`");

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe("<img onerror=alert(1)>");
  });

  it("renders an inline markdown image as a plain link, never an <img>", () => {
    renderContent("![a chart](https://example.invalid/chart.png)");

    expect(document.querySelector("img")).toBeNull();
    expect(screen.getByRole("link", { name: "a chart" })).toHaveAttribute(
      "href",
      "https://example.invalid/chart.png",
    );
  });

  it("drops an iframe without dropping the words around it", () => {
    const { container } = renderContent('before <iframe src="https://evil.invalid"></iframe> after');

    expect(container.querySelector("iframe")).toBeNull();
    expect(container.textContent).toContain("before");
    expect(container.textContent).toContain("after");
  });
});
