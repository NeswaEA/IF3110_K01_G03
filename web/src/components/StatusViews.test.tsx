import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiRequestError } from "../api/client";
import { ErrorState } from "./StatusViews";

describe("ErrorState", () => {
  it("presents 403 as a denial, not an empty result", () => {
    const err = new ApiRequestError(
      { code: "FORBIDDEN", message: "Akses edit diperlukan.", fields: {}, request_id: "req-9" },
      403,
    );
    render(<ErrorState error={err} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Akses ditolak");
    expect(screen.getByRole("alert")).toHaveTextContent("req-9");
  });
});
