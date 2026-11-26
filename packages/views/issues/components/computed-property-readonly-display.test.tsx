// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import {
  ComputedPropertyReadonlyDisplay,
  formatComputedPropertyValue,
} from "./computed-property-readonly-display";

describe("formatComputedPropertyValue", () => {
  it("renders numeric formulas and nested lookup arrays as readable text", () => {
    expect(formatComputedPropertyValue(42)).toBe("42");
    expect(formatComputedPropertyValue(3.5)).toBe("3.5");
    expect(formatComputedPropertyValue(["Alpha", "Beta"])).toBe("Alpha, Beta");
    expect(formatComputedPropertyValue(null)).toBe("");
  });
});

describe("ComputedPropertyReadonlyDisplay", () => {
  it("shows formula numbers with tabular styling", () => {
    renderWithI18n(<ComputedPropertyReadonlyDisplay value={128} />);
    expect(screen.getByText("128")).toHaveClass("tabular-nums");
  });

  it("shows lookup values as separate chips", () => {
    renderWithI18n(<ComputedPropertyReadonlyDisplay value={["One", "Two"]} />);
    expect(screen.getByText("One")).toBeInTheDocument();
    expect(screen.getByText("Two")).toBeInTheDocument();
  });

  it("shows localized empty copy for missing values", () => {
    renderWithI18n(<ComputedPropertyReadonlyDisplay value={undefined} />);
    expect(screen.getByText("Empty")).toBeInTheDocument();
  });
});