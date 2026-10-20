import { beforeEach, describe, expect, it, vi } from "vitest";

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiClient: { get, put },
}));

import {
  getPanelRateLimitSettings,
  updatePanelRateLimitSettings,
} from "@/api/admin/settings";

const settings = {
  enabled: true,
  user_rpm: 120,
  heavy_rpm: 30,
  exempt_admin: false,
  public_ip_rpm: 60,
};

describe("panel rate limit settings API", () => {
  beforeEach(() => {
    get.mockReset();
    put.mockReset();
    get.mockResolvedValue({ data: settings });
    put.mockResolvedValue({ data: settings });
  });

  it("gets the panel rate limit settings", async () => {
    await expect(getPanelRateLimitSettings()).resolves.toEqual(settings);
    expect(get).toHaveBeenCalledWith("/admin/settings/panel-rate-limit");
  });

  it("updates the full panel rate limit settings payload", async () => {
    await expect(updatePanelRateLimitSettings(settings)).resolves.toEqual(settings);
    expect(put).toHaveBeenCalledWith("/admin/settings/panel-rate-limit", settings);
  });
});
