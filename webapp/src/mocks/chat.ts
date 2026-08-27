import { http, HttpResponse } from "msw";

import type { components } from "../api/schema";

type Attachment = components["schemas"]["Attachment"];
type LinkPreview = components["schemas"]["LinkPreview"];
type Channel = components["schemas"]["Channel"];
type ChannelPage = components["schemas"]["ChannelPage"];
type Message = components["schemas"]["Message"];
type MessagePage = components["schemas"]["MessagePage"];
type UserSummary = components["schemas"]["UserSummary"];
type UserSummaryPage = components["schemas"]["UserSummaryPage"];
type MemberPage = components["schemas"]["MemberPage"];
type SearchPage = components["schemas"]["SearchPage"];
type SearchResult = components["schemas"]["SearchResult"];
type SendMessageRequest = components["schemas"]["SendMessageRequest"];
type EditMessageRequest = components["schemas"]["EditMessageRequest"];
type CreateChannelRequest = components["schemas"]["CreateChannelRequest"];
type OpenDirectMessageRequest = components["schemas"]["OpenDirectMessageRequest"];
type UpdateChannelRequest = components["schemas"]["UpdateChannelRequest"];
type AddChannelMemberRequest = components["schemas"]["AddChannelMemberRequest"];
type SetReadPositionRequest = components["schemas"]["SetReadPositionRequest"];
type ChannelCall = components["schemas"]["ChannelCall"];
type CallToken = components["schemas"]["CallToken"];
type ApiError = components["schemas"]["Error"];

/**
 * Contract mocks for the messaging and file surface, typed against the
 * generated schema. The fixture conversation mirrors the artboards
 * (chat-default's `#deploys`, the DM with Parisa, the empty `#design-tokens`)
 * so `VITE_API_MOCK=1 npm run dev` shows the design rather than lorem ipsum.
 *
 * `#general` carries a long history on purpose: it is the channel the
 * scrollback path is exercised against, and no artboard draws it in detail.
 */

export const CHAT_USERS = {
  me: {
    id: "00000000-0000-4000-8000-000000000001",
    username: "fixture.admin",
    display_name: "Fixture Admin",
  },
  nasrin: {
    id: "00000000-0000-4000-8000-0000000000a1",
    username: "nasrin",
    display_name: "Nasrin Ahmadi",
    presence: "online",
  },
  omid: {
    id: "00000000-0000-4000-8000-0000000000a2",
    username: "omid",
    display_name: "Omid Rezaei",
    presence: "away",
  },
  parisa: {
    id: "00000000-0000-4000-8000-0000000000a3",
    username: "parisa",
    display_name: "Parisa Kamali",
    presence: "offline",
  },
} as const satisfies Record<string, UserSummary>;

export const CHAT_CHANNELS = {
  general: "00000000-0000-4000-8000-0000000000c1",
  deploys: "00000000-0000-4000-8000-0000000000c2",
  designReview: "00000000-0000-4000-8000-0000000000c3",
  leads: "00000000-0000-4000-8000-0000000000c4",
  designTokens: "00000000-0000-4000-8000-0000000000c5",
  dmParisa: "00000000-0000-4000-8000-0000000000d1",
} as const;

/** Fixed instant so day separators and grouping are deterministic in tests. */
const TODAY = "2026-08-21";

/**
 * What the three cards on the component sheet render from — the fixed history
 * the artboards draw. Uploads made during a session come back from the upload
 * handler below with their own ids; these are the seeded ones.
 */
export const FIXTURE_FILE: Attachment = {
  id: "00000000-0000-4000-8000-0000000000f1",
  filename: "rollout-checklist.pdf",
  content_type: "application/pdf",
  size_bytes: 248 * 1024,
  url: "https://files.example.test/rollout-checklist.pdf",
};

/**
 * No `thumbnail_url`: the fixture is static, so a URL here would name bytes
 * the mock server does not have. The card draws its designed no-preview glyph
 * instead, which is a real drawn state rather than a broken image — and the
 * same state an expired thumbnail URL falls back to (AttachmentCards).
 *
 * Real attachment URLs are origin-relative (`/files/{id}?exp=…&sig=…`,
 * server/internal/filesign), so `img-src 'self'` covers them.
 */
export const FIXTURE_IMAGE: Attachment = {
  id: "00000000-0000-4000-8000-0000000000f2",
  filename: "latency-canary.png",
  content_type: "image/png",
  size_bytes: 1_260_000,
  width: 1600,
  height: 900,
  url: "https://files.example.test/latency-canary.png",
};

export const FIXTURE_LINK_PREVIEW: LinkPreview = {
  url: "https://status.example.test/incidents/482",
  title: "Canary latency writeup",
  description: "p99 held flat across the canary window.",
};

function at(time: string): string {
  return `${TODAY}T${time}:00.000Z`;
}

let sequence = 0;

function message(
  channelId: string,
  author: UserSummary,
  content: string,
  createdAt: string,
  extra: Partial<Message> = {},
): Message {
  sequence += 1;
  const suffix = String(sequence).padStart(12, "0");
  return {
    id: `00000000-0000-4000-8000-${suffix}`,
    channel_id: channelId,
    author,
    client_msg_id: `10000000-0000-4000-8000-${suffix}`,
    content,
    created_at: createdAt,
    attachments: [],
    ...extra,
  };
}

interface ChatState {
  channels: Channel[];
  messages: Map<string, Message[]>;
}

function seedState(): ChatState {
  sequence = 0;
  const { me, nasrin, omid, parisa } = CHAT_USERS;

  const channels: Channel[] = [
    {
      id: CHAT_CHANNELS.general,
      kind: "public",
      slug: "general",
      topic: "Everything that does not have a home yet",
      member_count: 22,
      unread_count: 0,
      mention_count: 0,
      created_at: "2026-01-05T09:00:00.000Z",
      created_by: nasrin.id,
    },
    {
      id: CHAT_CHANNELS.deploys,
      kind: "public",
      slug: "deploys",
      topic: "Release coordination and rollout notes",
      member_count: 14,
      unread_count: 2,
      mention_count: 0,
      created_at: "2026-02-01T09:00:00.000Z",
      created_by: nasrin.id,
    },
    {
      id: CHAT_CHANNELS.designReview,
      kind: "public",
      slug: "design-review",
      topic: "",
      member_count: 9,
      unread_count: 2,
      mention_count: 2,
      created_at: "2026-02-02T09:00:00.000Z",
      created_by: parisa.id,
    },
    {
      id: CHAT_CHANNELS.leads,
      kind: "private",
      slug: "leads",
      topic: "",
      member_count: 4,
      unread_count: 4,
      mention_count: 0,
      created_at: "2026-02-03T09:00:00.000Z",
      created_by: me.id,
    },
    {
      id: CHAT_CHANNELS.designTokens,
      kind: "public",
      slug: "design-tokens",
      topic: "",
      member_count: 1,
      unread_count: 0,
      mention_count: 0,
      created_at: `${TODAY}T08:00:00.000Z`,
      created_by: me.id,
    },
    {
      id: CHAT_CHANNELS.dmParisa,
      kind: "dm",
      slug: null,
      topic: "",
      member_count: 2,
      dm_peer: parisa,
      unread_count: 3,
      mention_count: 0,
      created_at: "2026-03-01T09:00:00.000Z",
      created_by: me.id,
    },
  ];

  const deploys = CHAT_CHANNELS.deploys;
  const deployMessages: Message[] = [
    message(deploys, nasrin, "Staging is green. Tag is `v1.2.0-rc3`", at("09:12")),
    message(deploys, nasrin, "Rolling to canary in ten minutes.", at("09:13")),
    message(deploys, me, "Nice. I'll watch the error rate.", at("09:14")),
    // The file card the chat-default artboard draws, seeded so the fixture
    // conversation shows what the artboard shows.
    message(deploys, omid, "Checklist for the rollout", at("09:20"), {
      edited_at: at("09:21"),
      attachments: [FIXTURE_FILE],
    }),
    message(deploys, parisa, "Latency held flat through the canary — writeup here.", at("09:31"), {
      link_preview: FIXTURE_LINK_PREVIEW,
    }),
    message(deploys, omid, "", at("09:33"), { deleted_at: at("09:34") }),
  ];
  // The unread divider sits before the first message after this one.
  const deploysChannel = channels.find((entry) => entry.id === deploys);
  if (deploysChannel !== undefined) {
    deploysChannel.last_read_message_id = deployMessages[3]?.id ?? null;
    deploysChannel.last_message_at = at("09:33");
  }

  const dm = CHAT_CHANNELS.dmParisa;
  const dmMessages: Message[] = [
    // The image card the chat-dm artboard draws.
    message(dm, parisa, "Sent you the latency capture from the canary window.", at("07:02"), {
      attachments: [FIXTURE_IMAGE],
    }),
    message(dm, me, "That is the flattest I have seen it. Adding it to the rollout note.", at("07:20")),
    message(dm, me, "Thank you.", at("07:21")),
  ];

  const general = CHAT_CHANNELS.general;
  const generalMessages: Message[] = [];
  for (let index = 0; index < 60; index += 1) {
    const minute = String(index % 60).padStart(2, "0");
    const hour = String(1 + Math.floor(index / 60)).padStart(2, "0");
    generalMessages.push(
      message(
        general,
        index % 2 === 0 ? nasrin : omid,
        `Backlog note ${String(index + 1)}`,
        at(`${hour}:${minute}`),
      ),
    );
  }

  return {
    channels,
    messages: new Map<string, Message[]>([
      [general, generalMessages],
      [deploys, deployMessages],
      [CHAT_CHANNELS.designReview, []],
      [CHAT_CHANNELS.leads, []],
      [CHAT_CHANNELS.designTokens, []],
      [dm, dmMessages],
    ]),
  };
}

let chat = seedState();

/**
 * Files uploaded but not yet attached to a message — the server's own
 * bookkeeping, so that an attachment_id naming nothing answers 404 here too.
 */
let uploaded = new Map<string, Attachment>();
let uploadSequence = 0;

/**
 * Live call state, per channel. There is no calls table on the server either
 * (ADR 005) — the media server's own room state is the truth, and this stands
 * in for it. Empty means nobody is in any call.
 */
let calls = new Map<string, ChannelCall>();

/**
 * Puts a call in a channel, the way one is already running before this client
 * ever opened the conversation. That is the case the REST read exists for.
 */
export function setMockCall(channelId: string, call: ChannelCall): void {
  calls.set(channelId, call);
}

/** Tests call this between cases to drop mock conversation state. */
export function resetMockChat(): void {
  chat = seedState();
  calls = new Map();
  uploaded = new Map();
  uploadSequence = 0;
}

/** The fixture history of one channel, for assertions in tests. */
export function mockMessages(channelId: string): Message[] {
  return chat.messages.get(channelId) ?? [];
}

export function mockChannel(channelId: string): Channel | undefined {
  return chat.channels.find((entry) => entry.id === channelId);
}

/**
 * The one multipart part `uploadFile` takes, parsed by hand.
 *
 * `request.formData()` is what this should be, but under jsdom the File the
 * app appends is jsdom's while the parser is Node's, and Node's asserts on its
 * own class — a browser has no such seam. Parsing the part here keeps the
 * workaround inside the mock instead of distorting the code under test.
 *
 * The same seam means a jsdom-created File arrives with its name and bytes
 * flattened to an anonymous empty blob, so no test may assert on the filename
 * or size a mocked upload comes back with. What a real browser sends is
 * asserted end to end instead (webapp/e2e/specs/chat-files.e2e.ts).
 */
function singleFilePart(
  raw: string,
): { filename: string; contentType: string; size: number } | null {
  const headEnd = raw.indexOf("\r\n\r\n");
  if (headEnd === -1) {
    return null;
  }
  const head = raw.slice(0, headEnd);
  const filename = /filename="([^"]*)"/u.exec(head)?.[1];
  if (filename === undefined || filename === "") {
    return null;
  }
  const closing = raw.lastIndexOf("\r\n--");
  const body = raw.slice(headEnd + 4, closing === -1 ? undefined : closing);
  return {
    filename,
    contentType: /content-type:\s*([^\r\n]+)/iu.exec(head)?.[1] ?? "application/octet-stream",
    size: new TextEncoder().encode(body).length,
  };
}

function errorResponse(status: number, code: string, message: string) {
  return HttpResponse.json<ApiError>({ error: { code, message } }, { status });
}

/** Every channel-scoped path answers 404 to a non-member, never 403. */
function notFound() {
  return errorResponse(404, "channel_not_found", "No such channel.");
}

function page(messages: Message[], url: URL): MessagePage {
  const limit = Number(url.searchParams.get("limit") ?? "50");
  const before = url.searchParams.get("before");
  const after = url.searchParams.get("after");
  const around = url.searchParams.get("around");

  let start: number;
  let end: number;
  if (before !== null) {
    const anchor = messages.findIndex((entry) => entry.id === before);
    end = anchor === -1 ? messages.length : anchor;
    start = Math.max(0, end - limit);
  } else if (after !== null) {
    const anchor = messages.findIndex((entry) => entry.id === after);
    start = anchor === -1 ? 0 : anchor + 1;
    end = Math.min(messages.length, start + limit);
  } else if (around !== null) {
    const anchor = messages.findIndex((entry) => entry.id === around);
    const centre = anchor === -1 ? messages.length - 1 : anchor;
    start = Math.max(0, centre - Math.floor(limit / 2));
    end = Math.min(messages.length, start + limit);
  } else {
    start = Math.max(0, messages.length - limit);
    end = messages.length;
  }

  const slice = messages.slice(start, end);
  const result: MessagePage = { messages: slice };
  if (start > 0 && slice[0] !== undefined) {
    result.before_cursor = slice[0].id;
  }
  if (end < messages.length && slice.at(-1) !== undefined) {
    result.after_cursor = slice[slice.length - 1]?.id ?? "";
  }
  return result;
}

function snippetFor(content: string, query: string): SearchResult["snippet"] {
  const index = content.toLowerCase().indexOf(query.toLowerCase());
  if (index === -1) {
    return { parts: [{ text: content, match: false }] };
  }
  return {
    parts: [
      { text: content.slice(0, index), match: false },
      { text: content.slice(index, index + query.length), match: true },
      { text: content.slice(index + query.length), match: false },
    ].filter((part) => part.text !== ""),
  };
}

export const chatHandlers = [
  http.get<never, never, ChannelPage>("/api/v1/channels", () =>
    HttpResponse.json({ channels: chat.channels }),
  ),

  http.post<never, CreateChannelRequest, Channel | ApiError>(
    "/api/v1/channels",
    async ({ request }) => {
      const body = await request.json();
      if (chat.channels.some((entry) => entry.slug === body.slug)) {
        return errorResponse(409, "channel_slug_taken", "That name is taken.");
      }
      const created: Channel = {
        id: `00000000-0000-4000-8000-${String(chat.channels.length).padStart(12, "9")}`,
        kind: body.kind,
        slug: body.slug,
        topic: body.topic ?? "",
        member_count: 1,
        unread_count: 0,
        mention_count: 0,
        created_at: new Date().toISOString(),
        created_by: CHAT_USERS.me.id,
      };
      chat.channels.push(created);
      chat.messages.set(created.id, []);
      return HttpResponse.json(created, { status: 201 });
    },
  ),

  http.post<never, OpenDirectMessageRequest, Channel | ApiError>(
    "/api/v1/dms",
    async ({ request }) => {
      const body = await request.json();
      const peer = Object.values(CHAT_USERS).find((entry) => entry.id === body.user_id);
      if (peer === undefined) {
        return errorResponse(404, "user_not_found", "No such user.");
      }
      const existing = chat.channels.find(
        (entry) => entry.kind === "dm" && entry.dm_peer?.id === peer.id,
      );
      if (existing !== undefined) {
        return HttpResponse.json(existing);
      }
      const created: Channel = {
        id: `00000000-0000-4000-8000-${String(chat.channels.length).padStart(12, "8")}`,
        kind: "dm",
        slug: null,
        topic: "",
        member_count: 2,
        dm_peer: peer,
        unread_count: 0,
        mention_count: 0,
        created_at: new Date().toISOString(),
        created_by: CHAT_USERS.me.id,
      };
      chat.channels.push(created);
      chat.messages.set(created.id, []);
      return HttpResponse.json(created, { status: 201 });
    },
  ),

  http.get<{ channelId: string }, never, Channel | ApiError>(
    "/api/v1/channels/:channelId",
    ({ params }) => {
      const channel = mockChannel(params.channelId);
      return channel === undefined ? notFound() : HttpResponse.json(channel);
    },
  ),

  http.patch<{ channelId: string }, UpdateChannelRequest, Channel | ApiError>(
    "/api/v1/channels/:channelId",
    async ({ params, request }) => {
      const channel = mockChannel(params.channelId);
      if (channel === undefined) {
        return notFound();
      }
      if (channel.kind === "dm") {
        return errorResponse(400, "invalid_request", "A direct message has no topic.");
      }
      channel.topic = (await request.json()).topic;
      return HttpResponse.json(channel);
    },
  ),

  http.get<{ channelId: string }, never, ChannelCall | ApiError>(
    "/api/v1/channels/:channelId/call",
    ({ params }) => {
      if (mockChannel(params.channelId) === undefined) {
        return notFound();
      }
      // "No call here" is a 200 with active false, not a 404: the channel is
      // real and the answer about it is "nobody is in one".
      return HttpResponse.json(calls.get(params.channelId) ?? { active: false });
    },
  ),

  http.post<{ channelId: string }, never, CallToken | ApiError>(
    "/api/v1/channels/:channelId/call/token",
    ({ params }) => {
      if (mockChannel(params.channelId) === undefined) {
        return notFound();
      }
      return HttpResponse.json(
        {
          token: "fixture-call-ticket-not-a-real-one",
          room: `chan-${params.channelId}`,
          // Two minutes: a ticket has no business outliving the click that
          // asked for it (openapi.yaml, createCallToken).
          expires_at: new Date(Date.now() + 2 * 60 * 1000).toISOString(),
        },
        { status: 201 },
      );
    },
  ),

  http.get<{ channelId: string }, never, MemberPage | ApiError>(
    "/api/v1/channels/:channelId/members",
    ({ params }) =>
      mockChannel(params.channelId) === undefined
        ? notFound()
        : HttpResponse.json({ members: Object.values(CHAT_USERS) }),
  ),

  http.post<{ channelId: string }, AddChannelMemberRequest, ApiError | null>(
    "/api/v1/channels/:channelId/members",
    ({ params }) => {
      const channel = mockChannel(params.channelId);
      if (channel === undefined) {
        return notFound();
      }
      channel.member_count += 1;
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.get<{ channelId: string }, never, MessagePage | ApiError>(
    "/api/v1/channels/:channelId/messages",
    ({ params, request }) => {
      const messages = chat.messages.get(params.channelId);
      if (messages === undefined) {
        return notFound();
      }
      return HttpResponse.json(page(messages, new URL(request.url)));
    },
  ),

  http.post<{ channelId: string }, SendMessageRequest, Message | ApiError>(
    "/api/v1/channels/:channelId/messages",
    async ({ params, request }) => {
      const messages = chat.messages.get(params.channelId);
      if (messages === undefined) {
        return notFound();
      }
      const body = await request.json();
      // Idempotent on client_msg_id: a resend returns the stored message.
      const existing = messages.find((entry) => entry.client_msg_id === body.client_msg_id);
      if (existing !== undefined) {
        return HttpResponse.json(existing, { status: 200 });
      }
      if (body.content === "" && (body.attachment_ids ?? []).length === 0) {
        return errorResponse(400, "invalid_request", "A message needs text or files.");
      }
      const attachments = (body.attachment_ids ?? []).map((id) => uploaded.get(id));
      if (attachments.some((entry) => entry === undefined)) {
        return errorResponse(404, "attachment_not_found", "No such attachment.");
      }
      const created = message(
        params.channelId,
        CHAT_USERS.me,
        body.content,
        new Date().toISOString(),
        { attachments: attachments.filter((entry) => entry !== undefined) },
      );
      created.client_msg_id = body.client_msg_id;
      messages.push(created);
      return HttpResponse.json(created, { status: 201 });
    },
  ),

  http.post<{ channelId: string }, never, Attachment | ApiError>(
    "/api/v1/channels/:channelId/files",
    async ({ params, request }) => {
      if (mockChannel(params.channelId) === undefined) {
        return notFound();
      }
      const file = singleFilePart(await request.text());
      if (file === null) {
        return errorResponse(400, "invalid_request", "No file part.");
      }
      // The size cap is the instance document's; the mock instance serves the
      // same 25 MiB default the fallback assumes.
      if (file.size > 25 * 1024 * 1024) {
        return errorResponse(413, "file_too_large", "The file is too large.");
      }
      uploadSequence += 1;
      const id = `00000000-0000-4000-8000-${String(uploadSequence).padStart(12, "f")}`;
      const attachment: Attachment = {
        id,
        filename: file.filename,
        content_type: file.contentType,
        size_bytes: file.size,
        // Origin-relative and signature-shaped, exactly as filesign mints it.
        url: `/files/${id}?exp=0&sig=mock`,
      };
      uploaded.set(id, attachment);
      return HttpResponse.json(attachment, { status: 201 });
    },
  ),

  http.patch<{ channelId: string; messageId: string }, EditMessageRequest, Message | ApiError>(
    "/api/v1/channels/:channelId/messages/:messageId",
    async ({ params, request }) => {
      const messages = chat.messages.get(params.channelId);
      const target = messages?.find((entry) => entry.id === params.messageId);
      if (messages === undefined || target === undefined) {
        return notFound();
      }
      if (target.author.id !== CHAT_USERS.me.id) {
        return errorResponse(403, "not_message_author", "Not your message.");
      }
      target.content = (await request.json()).content;
      target.edited_at = new Date().toISOString();
      return HttpResponse.json(target);
    },
  ),

  http.delete<{ channelId: string; messageId: string }, never, ApiError | null>(
    "/api/v1/channels/:channelId/messages/:messageId",
    ({ params }) => {
      const messages = chat.messages.get(params.channelId);
      const target = messages?.find((entry) => entry.id === params.messageId);
      if (messages === undefined || target === undefined) {
        return notFound();
      }
      target.content = "";
      target.deleted_at = new Date().toISOString();
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.put<{ channelId: string }, SetReadPositionRequest, ApiError | null>(
    "/api/v1/channels/:channelId/read",
    async ({ params, request }) => {
      const channel = mockChannel(params.channelId);
      if (channel === undefined) {
        return notFound();
      }
      channel.last_read_message_id = (await request.json()).message_id;
      channel.unread_count = 0;
      channel.mention_count = 0;
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.get<never, never, UserSummaryPage>("/api/v1/users", ({ request }) => {
    const query = (new URL(request.url).searchParams.get("q") ?? "").toLowerCase();
    const users = Object.values(CHAT_USERS).filter(
      (user) =>
        query === "" ||
        user.username.includes(query) ||
        user.display_name.toLowerCase().includes(query),
    );
    return HttpResponse.json({ users });
  }),

  http.get<never, never, SearchPage>("/api/v1/search", ({ request }) => {
    const url = new URL(request.url);
    const query = url.searchParams.get("q") ?? "";
    const kind = url.searchParams.get("kind") === "files" ? "files" : "messages";
    if (kind === "files") {
      // Accepted from Phase 1.2, empty until the upload pipeline lands.
      return HttpResponse.json({ kind, results: [], total: 0, total_capped: false });
    }
    const results: SearchResult[] = [];
    for (const channel of chat.channels) {
      for (const entry of chat.messages.get(channel.id) ?? []) {
        if (
          entry.deleted_at === undefined &&
          query !== "" &&
          entry.content.toLowerCase().includes(query.toLowerCase())
        ) {
          results.push({
            message_id: entry.id,
            channel: {
              id: channel.id,
              kind: channel.kind,
              slug: channel.slug ?? null,
              ...(channel.dm_peer === undefined ? {} : { dm_peer: channel.dm_peer }),
            },
            author: entry.author,
            created_at: entry.created_at,
            snippet: snippetFor(entry.content, query),
          });
        }
      }
    }
    return HttpResponse.json({
      kind: "messages",
      results,
      total: results.length,
      total_capped: false,
    });
  }),
];
