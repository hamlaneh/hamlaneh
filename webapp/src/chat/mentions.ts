import type { Element, ElementContent, Root, RootContent } from "hast";

/**
 * Mentions travel on the wire as the literal token `<@{user_id}>` inside
 * message content (openapi.yaml -> SendMessageRequest.content). The id is the
 * contract; the display name is only a rendering. Never parse display names —
 * they are neither unique nor stable, and in Persian they cannot match the
 * username pattern at all.
 *
 * `<@` is not a valid HTML tag start and not a valid autolink, so CommonMark
 * leaves the token as literal text and this plugin is what turns it into a
 * mention element.
 */

const UUID = "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}";
const MENTION_PATTERN = new RegExp(`<@(${UUID})>`, "g");

/** Elements whose text is literal by definition — a mention inside a code span stays a token. */
const LITERAL_ELEMENTS = new Set(["code", "pre"]);

export interface MentionSegment {
  kind: "text" | "mention";
  value: string;
}

/** Splits a string into plain runs and mention tokens (the user id is the value). */
export function splitMentions(text: string): MentionSegment[] {
  const segments: MentionSegment[] = [];
  let cursor = 0;
  // A fresh regex per call: a shared /g regex carries lastIndex between calls.
  const pattern = new RegExp(MENTION_PATTERN.source, "g");
  let match = pattern.exec(text);
  while (match !== null) {
    if (match.index > cursor) {
      segments.push({ kind: "text", value: text.slice(cursor, match.index) });
    }
    segments.push({ kind: "mention", value: match[1] ?? "" });
    cursor = match.index + match[0].length;
    match = pattern.exec(text);
  }
  if (cursor < text.length) {
    segments.push({ kind: "text", value: text.slice(cursor) });
  }
  return segments;
}

/** Resolves a user id to the name to draw. Returns null when the user is unknown. */
export type MentionResolver = (userId: string) => string | null;

function mentionElement(userId: string, label: string): Element {
  return {
    type: "element",
    tagName: "span",
    properties: { className: ["hm-mention"], "data-user-id": userId },
    children: [{ type: "text", value: `@${label}` }],
  };
}

function transformChildren(
  children: RootContent[],
  resolve: MentionResolver,
  unknownLabel: string,
  literal: boolean,
): RootContent[] {
  const next: RootContent[] = [];
  for (const child of children) {
    if (child.type === "element") {
      next.push({
        ...child,
        children: transformChildren(
          child.children,
          resolve,
          unknownLabel,
          literal || LITERAL_ELEMENTS.has(child.tagName),
        ) as ElementContent[],
      });
      continue;
    }
    if (child.type !== "text" || literal) {
      next.push(child);
      continue;
    }
    const segments = splitMentions(child.value);
    if (segments.length === 1 && segments[0]?.kind === "text") {
      next.push(child);
      continue;
    }
    for (const segment of segments) {
      if (segment.kind === "text") {
        next.push({ type: "text", value: segment.value });
      } else {
        next.push(mentionElement(segment.value, resolve(segment.value) ?? unknownLabel));
      }
    }
  }
  return next;
}

export interface MentionOptions {
  resolve: MentionResolver;
  /** Drawn when the id belongs to nobody the client can name. */
  unknownLabel: string;
}

/**
 * Rehype plugin that replaces mention tokens with a mention span.
 *
 * Runs AFTER the sanitizer on purpose: the spans it inserts are built here
 * from a matched uuid and a resolved display name, so they are safe by
 * construction and must not be stripped by an allowlist that (correctly)
 * knows nothing about them.
 */
export function rehypeMentions(options: MentionOptions) {
  return function transform(tree: Root): void {
    tree.children = transformChildren(
      tree.children,
      options.resolve,
      options.unknownLabel,
      false,
    );
  };
}
