import { describe, expect, it } from "vitest";
import * as fmt from "./format";

describe("отсутствие данных", () => {
  it("никогда не превращается в ноль", () => {
    for (const f of [fmt.tokens, fmt.usd, fmt.percent, fmt.duration]) {
      expect(f(null)).toBe(fmt.NO_VALUE);
      expect(f(undefined)).toBe(fmt.NO_VALUE);
      expect(f(Number.NaN)).toBe(fmt.NO_VALUE);
    }
    expect(fmt.model(null)).toBe(fmt.NO_VALUE);
  });

  it("настоящий ноль показывается как ноль", () => {
    expect(fmt.tokens(0)).toBe("0");
    expect(fmt.usd(0)).toBe("≈$0.00");
    expect(fmt.percent(0)).toBe("0%");
    expect(fmt.duration(0)).toBe("0с");
  });
});

describe("tokens", () => {
  it.each([
    [999, "999"],
    [1189, "1.2k"],
    [25553, "25.6k"],
    [99949, "99.9k"],
    [99950, "100k"],
    [120621, "121k"],
    [999499, "999k"],
    [999500, "1.0M"],
    [1234567, "1.2M"],
  ])("%d → %s", (input, want) => expect(fmt.tokens(input)).toBe(want));
});

describe("usd", () => {
  it.each([
    [0.0574738, "≈$0.06"],
    [0.001, "≈$<0.01"],
    [12.345, "≈$12.35"],
    [250.7, "≈$251"],
  ])("%d → %s", (input, want) => expect(fmt.usd(input)).toBe(want));
});

describe("duration", () => {
  it.each([
    [45, "45с"],
    [900, "15м"],
    [7500, "2ч 05м"],
    [15169, "4ч 12м"],
    [482568, "5д 14ч"],
    [-5, "0с"],
  ])("%d → %s", (input, want) => expect(fmt.duration(input)).toBe(want));
});

describe("millis", () => {
  it.each([
    [850, "850мс"],
    [6604, "6.6с"],
    [95_400, "95с"],
  ])("%d → %s", (input, want) => expect(fmt.millis(input)).toBe(want));
  it("null — прочерк", () => expect(fmt.millis(null)).toBe(fmt.NO_VALUE));
});

describe("percent и model", () => {
  it("помечает оценку тильдой", () => {
    expect(fmt.percent(25.6)).toBe("26%");
    expect(fmt.percent(25.6, true)).toBe("~26%");
  });

  it("сокращает имя модели", () => {
    expect(fmt.model("claude-sonnet-5-5")).toBe("sonnet 5.5");
    expect(fmt.model("claude-sonnet-5-5[1m]")).toBe("sonnet 5.5[1m]");
    expect(fmt.model("something-else")).toBe("something-else");
  });
});
