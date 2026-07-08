import { beforeEach, describe, expect, it, vi } from "vitest";

const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiClient: {
    get,
  },
}));

import { getRegion } from "@/api/region";

describe("region api", () => {
  beforeEach(() => {
    get.mockReset();
  });

  it("calls the public region endpoint", async () => {
    const response = {
      country_code: "CN",
      user_region_notice_enabled: true,
    };

    get.mockResolvedValue({ data: response });

    await expect(getRegion()).resolves.toEqual(response);
    expect(get).toHaveBeenCalledWith("/region");
  });
});
