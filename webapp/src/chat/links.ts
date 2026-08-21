/**
 * The message permalink the "Copy link" action writes to the clipboard, and
 * the route the search results link to. Its `/m/:messageId` segment is what
 * the history request resolves with the contract's `around` cursor.
 */
export function messageLink(channelId: string, messageId: string): string {
  return `${window.location.origin}/c/${channelId}/m/${messageId}`;
}
