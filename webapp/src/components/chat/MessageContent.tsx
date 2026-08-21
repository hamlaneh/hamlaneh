import { memo, useMemo } from "react";
import { useTranslation } from "react-i18next";
import Markdown from "react-markdown";
import type { Components } from "react-markdown";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import type { Options as SanitizeSchema } from "rehype-sanitize";
import type { PluggableList } from "unified";

import { rehypeMentions } from "../../chat/mentions";
import type { MentionResolver } from "../../chat/mentions";

/**
 * Message bodies are untrusted markdown authored by other users.
 *
 * Three independent defences, in this order:
 *  1. Raw HTML never reaches the tree — remark-rehype drops `html` nodes
 *     because `allowDangerousHtml` is off (the default) and no rehype-raw
 *     plugin is installed, so `<script>` is not parsed as markup at all.
 *  2. `rehype-sanitize` runs over the resulting tree with an allowlist cut
 *     down to the elements the design actually draws, and an href/src
 *     protocol allowlist.
 *  3. `urlTransform` refuses any scheme outside http/https/mailto before a
 *     link is rendered, so `javascript:` never lands in the DOM.
 *
 * react-markdown builds React elements — there is no `dangerouslySetInnerHTML`
 * anywhere in this path, which is also what keeps the strict CSP happy.
 */

/** Exactly the constructs "markdown inside a bubble" draws, plus img (see below). */
const ALLOWED_TAGS = [
  "p",
  "br",
  "strong",
  "em",
  "code",
  "pre",
  "blockquote",
  "ol",
  "ul",
  "li",
  "a",
  "img",
];

/**
 * hast-util-sanitize replaces a disallowed element with its children, so an
 * out-of-scope construct (a heading, say) degrades to its text instead of
 * disappearing. Only childless elements are lost outright.
 */
const SANITIZE_SCHEMA: SanitizeSchema = {
  ...defaultSchema,
  tagNames: ALLOWED_TAGS,
  attributes: {
    a: ["href", "title"],
    img: ["src", "alt", "title"],
  },
  protocols: {
    href: ["http", "https", "mailto"],
    src: ["http", "https"],
  },
};

const ALLOWED_SCHEMES = new Set(["http:", "https:", "mailto:"]);

/**
 * Returns the URL when its scheme is allowed (or it is relative), and an empty
 * string otherwise. Relative URLs are detected the way the CommonMark spec
 * does: a colon that comes after the first `/`, `?` or `#` is not a scheme.
 */
function safeUrl(url: string): string {
  const value = url.trim();
  const colon = value.indexOf(":");
  if (colon === -1) {
    return value;
  }
  for (const delimiter of ["/", "?", "#"]) {
    const index = value.indexOf(delimiter);
    if (index !== -1 && index < colon) {
      return value;
    }
  }
  return ALLOWED_SCHEMES.has(value.slice(0, colon + 1).toLowerCase()) ? value : "";
}

interface MessageContentProps {
  content: string;
  /** Maps a mention's user id to the name to draw. */
  resolveMention: MentionResolver;
}

/**
 * A message body. `dir="auto"` per the handoff: a Persian message in an
 * English workspace still reads correctly, and vice versa.
 *
 * Memoized: parsing markdown and running the plugin pipeline is by far the
 * most expensive thing the conversation does per render.
 */
export const MessageContent = memo(function MessageContent({
  content,
  resolveMention,
}: MessageContentProps) {
  const { t } = useTranslation();
  const unknownLabel = t("chat.messages.unknownMember");

  const components = useMemo<Components>(
    () => ({
      a: ({ children, href, title }) =>
        href === undefined || href === "" ? (
          <span>{children}</span>
        ) : (
          <a
            className="hm-md__link"
            href={href}
            title={title}
            // Untrusted third-party destination: no opener, no referrer,
            // and marked as user-generated for crawlers.
            target="_blank"
            rel="noopener noreferrer nofollow ugc"
          >
            {children}
          </a>
        ),
      // Inline images are not a designed surface, and loading one would leak
      // the reader's IP to whatever host the author chose. The image is
      // rendered as a plain link instead, so nothing is silently dropped.
      img: ({ src, alt, title }) => {
        const target = typeof src === "string" ? safeUrl(src) : "";
        const label = alt !== undefined && alt !== "" ? alt : (title ?? target);
        return target === "" ? (
          <span>{label}</span>
        ) : (
          <a
            className="hm-md__link"
            href={target}
            target="_blank"
            rel="noopener noreferrer nofollow ugc"
          >
            {label}
          </a>
        );
      },
    }),
    [],
  );

  // Tuples, not calls: unified takes `[plugin, options]` and applies the
  // plugins in order — sanitize first, then decorate.
  const rehypePlugins = useMemo<PluggableList>(
    () => [
      [rehypeSanitize, SANITIZE_SCHEMA],
      [rehypeMentions, { resolve: resolveMention, unknownLabel }],
    ],
    [resolveMention, unknownLabel],
  );

  return (
    <div className="hm-md" dir="auto">
      <Markdown
        skipHtml
        urlTransform={safeUrl}
        components={components}
        rehypePlugins={rehypePlugins}
      >
        {content}
      </Markdown>
    </div>
  );
});
