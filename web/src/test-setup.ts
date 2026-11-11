import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterAll, afterEach, beforeAll } from "vitest";
import { resetCsrf } from "./api/client";
import { FakeEventSource, server } from "./test-utils";

// jsdom lacks these browser APIs used by the layout and Radix.
globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
window.matchMedia ??= (query: string) =>
  ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList;
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
Element.prototype.scrollIntoView ??= () => {};
Element.prototype.hasPointerCapture ??= () => false;
window.scrollTo = () => {};

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => {
  // vitest runs without globals, so Testing Library cannot register its own cleanup
  cleanup();
  server.resetHandlers();
  resetCsrf();
  FakeEventSource.instances.length = 0;
});
afterAll(() => server.close());
