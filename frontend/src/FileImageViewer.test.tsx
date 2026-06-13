import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";

import { FileImageViewer } from "./FileImageViewer";

test("image previews expose a stable route through the native link menu", () => {
  render(
    <FileImageViewer
      src="blob:https://tank.example/blob-id"
      alt="screenshots/result.png"
      openHref="https://tank.example/sessions/42/files/screenshots/result.png"
    />,
  );

  const link = screen.getByRole("link", { name: "screenshots/result.png" });
  const image = screen.getByRole("img", { name: "screenshots/result.png" });

  expect(link).toHaveAttribute(
    "href",
    "https://tank.example/sessions/42/files/screenshots/result.png",
  );
  expect(link).toHaveAttribute("target", "_blank");
  expect(image).toHaveAttribute("src", "blob:https://tank.example/blob-id");
});

test("plain clicks stay in the viewer while modified clicks remain browser-native", () => {
  render(
    <FileImageViewer
      src="blob:https://tank.example/blob-id"
      alt="screenshots/result.png"
      openHref="https://tank.example/sessions/42/files/screenshots/result.png"
    />,
  );

  const link = screen.getByRole("link", { name: "screenshots/result.png" });

  expect(fireEvent.click(link, { button: 0 })).toBe(false);
  expect(fireEvent.click(link, { button: 0, ctrlKey: true })).toBe(true);
  expect(fireEvent.click(link, { button: 0, metaKey: true })).toBe(true);
});
