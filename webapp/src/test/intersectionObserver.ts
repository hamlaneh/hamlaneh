/**
 * jsdom has no layout, so it ships no IntersectionObserver. The message list
 * uses one to ask for the previous page of history as the top of the
 * conversation scrolls into view; this stub records the observed elements so a
 * test can trigger that moment deliberately.
 */

type ObserverCallback = (entries: IntersectionObserverEntry[]) => void;

const observed = new Map<Element, ObserverCallback>();

class StubIntersectionObserver implements IntersectionObserver {
  readonly root: Element | Document | null = null;
  readonly rootMargin = "0px";
  readonly scrollMargin = "0px";
  readonly thresholds: readonly number[] = [0];

  private readonly callback: ObserverCallback;
  private readonly elements = new Set<Element>();

  constructor(callback: ObserverCallback) {
    this.callback = callback;
  }

  observe(target: Element): void {
    this.elements.add(target);
    observed.set(target, this.callback);
  }

  unobserve(target: Element): void {
    this.elements.delete(target);
    observed.delete(target);
  }

  disconnect(): void {
    for (const element of this.elements) {
      observed.delete(element);
    }
    this.elements.clear();
  }

  takeRecords(): IntersectionObserverEntry[] {
    return [];
  }
}

export function installIntersectionObserverStub(): void {
  globalThis.IntersectionObserver =
    StubIntersectionObserver as unknown as typeof IntersectionObserver;
}

/** Fires an intersection for one observed element, as scrolling to it would. */
export function scrollIntoIntersection(target: Element): void {
  const callback = observed.get(target);
  if (callback === undefined) {
    throw new Error("Element is not being observed by an IntersectionObserver");
  }
  callback([{ isIntersecting: true, target } as unknown as IntersectionObserverEntry]);
}
