import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import "@multica/vitest-setup/web-storage";

afterEach(() => {
  cleanup();
});
