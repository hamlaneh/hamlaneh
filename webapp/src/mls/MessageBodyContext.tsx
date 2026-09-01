import { createContext, useContext } from "react";
import type { ReactNode } from "react";

import type { Message } from "../chat/types";
import type { MessageBody } from "./types";

/**
 * How a bubble learns what to draw.
 *
 * A context rather than a prop because the alternative is threading a
 * decryption lookup through the shell, the list, the day groups and the run
 * groups to reach one component. The default resolves every message as
 * plaintext, so a tree without a provider — every existing test, and the whole
 * plaintext app — behaves exactly as it did.
 */
const MessageBodyContext = createContext<(message: Message) => MessageBody>((message) => ({
  kind: "plaintext",
  text: message.content,
}));

export function MessageBodyProvider({
  resolve,
  children,
}: {
  resolve: (message: Message) => MessageBody;
  children: ReactNode;
}) {
  return <MessageBodyContext.Provider value={resolve}>{children}</MessageBodyContext.Provider>;
}

/** The body of one message: plaintext, decrypted, still pending, or refused. */
export function useMessageBody(message: Message): MessageBody {
  return useContext(MessageBodyContext)(message);
}
