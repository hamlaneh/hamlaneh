import "@testing-library/jest-dom/vitest";

import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";

import { installIntersectionObserverStub } from "./intersectionObserver";

installIntersectionObserverStub();

/**
 * jsdom implements no layout and therefore no scrollIntoView. The message list
 * calls it when a permalink lands on a message; without this the call is a
 * TypeError that takes the whole tree down.
 */
if (typeof Element.prototype.scrollIntoView !== "function") {
  Element.prototype.scrollIntoView = () => undefined;
}

beforeEach(() => {
  // The chat shell is behind a router, so a case that navigated would
  // otherwise hand the next one its URL.
  window.history.replaceState({}, "", "/");
});

afterEach(() => {
  cleanup();
});
