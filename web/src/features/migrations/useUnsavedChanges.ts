import { useCallback, useEffect, useRef } from "react";

// Keep unsent SQL in memory and let the browser warn before document navigation.
export function useUnsavedChanges(enabled: boolean): () => void {
  const shouldBlock = useRef(enabled);
  shouldBlock.current = enabled;

  useEffect(() => {
    if (!enabled) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!shouldBlock.current) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [enabled]);

  // A successful save may navigate before React has committed the saved state.
  return useCallback(() => { shouldBlock.current = false; }, []);
}
