import { afterEach, expect, test, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("./auth", () => ({ authedFetch: vi.fn() }));

import { authedFetch } from "./auth";
import { AdminDataBrowser, formatBytes } from "./AdminDataBrowser";

function jsonResponse(body: unknown, ok = true): Response {
  return {
    ok,
    status: ok ? 200 : 500,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

const tablesBody = {
  tables: [
    { name: "profiles", est_rows: 12 },
    { name: "stream_auth_tickets", est_rows: 5 },
  ],
};

// One row whose secret bearer column is already redacted server-side, plus a
// bytea column the server returns as a byte count, plus a plain value.
const rowsBody = {
  table: "stream_auth_tickets",
  columns: [
    { name: "ticket", type: "text", kind: "redacted" },
    { name: "email", type: "text", kind: "value" },
    { name: "blob", type: "bytea", kind: "bytes" },
  ],
  primary_key: ["ticket"],
  rows: [["‹redacted›", "owner@example.com", 2048]],
  has_more: false,
  next_cursor: "",
  est_total: 5,
  paginated: true,
};

afterEach(() => {
  vi.clearAllMocks();
});

test("lists tables, then renders a selected table's rows with redaction", async () => {
  vi.mocked(authedFetch).mockImplementation((input) =>
    Promise.resolve(
      String(input).includes("/rows")
        ? jsonResponse(rowsBody)
        : jsonResponse(tablesBody),
    ),
  );
  const user = userEvent.setup();
  render(<AdminDataBrowser />);

  // Table directory populates from /api/admin/data/tables.
  expect(await screen.findByText("profiles")).toBeInTheDocument();
  await user.click(screen.getByText("stream_auth_tickets"));

  // Columns render from the rows response.
  expect(await screen.findByText("ticket")).toBeInTheDocument();
  expect(screen.getByText("email")).toBeInTheDocument();

  // Plain value is shown; the bytea column shows a size; the secret column
  // shows the masked chip and never the underlying bearer value.
  expect(screen.getByText("owner@example.com")).toBeInTheDocument();
  expect(screen.getByText("2.0 KB")).toBeInTheDocument();
  expect(screen.getByText("redacted")).toBeInTheDocument();
});

test("surfaces an error banner when the table list request fails", async () => {
  vi.mocked(authedFetch).mockResolvedValue(
    jsonResponse({ detail: "admin access required" }, false),
  );
  render(<AdminDataBrowser />);
  expect(
    await screen.findByText("admin access required"),
  ).toBeInTheDocument();
});

test("formatBytes renders human sizes", () => {
  expect(formatBytes(512)).toBe("512 B");
  expect(formatBytes(2048)).toBe("2.0 KB");
  expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MB");
  expect(formatBytes("not-a-number")).toBe("not-a-number");
});
