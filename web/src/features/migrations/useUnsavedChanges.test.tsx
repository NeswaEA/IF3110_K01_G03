import { act, renderHook } from "@testing-library/react";
import { StrictMode } from "react";
import { expect, it } from "vitest";
import { useUnsavedChanges } from "./useUnsavedChanges";

function navigationBlocked() {
  const event = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

it("only guards unsaved input and removes the listener on save and unmount in StrictMode", () => {
  const { rerender, unmount } = renderHook(({ dirty }) => useUnsavedChanges(dirty), {
    initialProps: { dirty: false }, wrapper: StrictMode,
  });
  expect(navigationBlocked()).toBe(false);
  rerender({ dirty: true });
  expect(navigationBlocked()).toBe(true);
  rerender({ dirty: false });
  expect(navigationBlocked()).toBe(false);
  rerender({ dirty: true });
  expect(navigationBlocked()).toBe(true);
  unmount();
  expect(navigationBlocked()).toBe(false);
});

it("allows a successful save to navigate immediately before React renders the saved state", () => {
  const { result, rerender } = renderHook(({ dirty }) => useUnsavedChanges(dirty), { initialProps: { dirty: true } });
  expect(navigationBlocked()).toBe(true);
  act(() => result.current());
  expect(navigationBlocked()).toBe(false);
  rerender({ dirty: false });
  expect(navigationBlocked()).toBe(false);
  rerender({ dirty: true });
  expect(navigationBlocked()).toBe(true);
});
