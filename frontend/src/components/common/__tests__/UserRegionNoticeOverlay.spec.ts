import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { nextTick, reactive } from "vue";

import UserRegionNoticeOverlay from "@/components/common/UserRegionNoticeOverlay.vue";

const mocks = vi.hoisted(() => ({
  route: undefined as unknown as {
    fullPath: string;
    meta: Record<string, unknown>;
  },
  authStore: undefined as unknown as {
    isAuthenticated: boolean;
    isAdmin: boolean;
  },
  getRegion: vi.fn(),
}));

vi.mock("vue-router", () => ({
  useRoute: () => mocks.route,
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock("@/stores", () => ({
  useAuthStore: () => mocks.authStore,
}));

vi.mock("@/api/region", () => ({
  getRegion: mocks.getRegion,
}));

function resetEligibility() {
  mocks.route = reactive({
    fullPath: "/dashboard",
    meta: {
      requiresAuth: true,
      requiresAdmin: false,
    } as Record<string, unknown>,
  });
  mocks.authStore = reactive({
    isAuthenticated: true,
    isAdmin: false,
  });
}

function setPublicRoute() {
  mocks.route.meta = {
    ...mocks.route.meta,
    requiresAuth: false,
  };
}

function setAdminRoute() {
  mocks.route.meta = {
    requiresAuth: true,
    requiresAdmin: true,
  };
}

async function mountAndFlush() {
  const wrapper = mount(UserRegionNoticeOverlay);
  await flushPromises();
  return wrapper;
}

describe("UserRegionNoticeOverlay", () => {
  beforeEach(() => {
    resetEligibility();
    mocks.getRegion.mockReset();
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("shows for eligible signed-in users when the feature is enabled in CN", async () => {
    mocks.getRegion.mockResolvedValue({
      country_code: "CN",
      user_region_notice_enabled: true,
    });

    const wrapper = await mountAndFlush();

    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(true);
    expect(mocks.getRegion).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it.each([["HK"], ["TW"], ["MO"], [""]])(
    "stays hidden for non-triggering country code %s",
    async (countryCode) => {
      mocks.getRegion.mockResolvedValue({
        country_code: countryCode,
        user_region_notice_enabled: true,
      });

      const wrapper = await mountAndFlush();

      expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
      wrapper.unmount();
    },
  );

  it("stays hidden when the backend setting is disabled", async () => {
    mocks.getRegion.mockResolvedValue({
      country_code: "CN",
      user_region_notice_enabled: false,
    });

    const wrapper = await mountAndFlush();

    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("fails open when the region check fails", async () => {
    mocks.getRegion.mockRejectedValue(new Error("network"));

    const wrapper = await mountAndFlush();

    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("does not check region on admin routes", async () => {
    setAdminRoute();

    const wrapper = await mountAndFlush();

    expect(mocks.getRegion).not.toHaveBeenCalled();
    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("does not check region on public routes", async () => {
    setPublicRoute();

    const wrapper = await mountAndFlush();

    expect(mocks.getRegion).not.toHaveBeenCalled();
    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("does not check region for admin users", async () => {
    mocks.authStore.isAdmin = true;

    const wrapper = await mountAndFlush();

    expect(mocks.getRegion).not.toHaveBeenCalled();
    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("polls the region endpoint while the user stays eligible", async () => {
    vi.useFakeTimers();
    mocks.getRegion.mockResolvedValue({
      country_code: "HK",
      user_region_notice_enabled: true,
    });

    const wrapper = await mountAndFlush();

    expect(mocks.getRegion).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(30_000);
    await flushPromises();
    expect(mocks.getRegion).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("rechecks on route changes and updates visibility", async () => {
    mocks.getRegion
      .mockResolvedValueOnce({
        country_code: "HK",
        user_region_notice_enabled: true,
      })
      .mockResolvedValueOnce({
        country_code: "CN",
        user_region_notice_enabled: true,
      });

    const wrapper = await mountAndFlush();

    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);

    mocks.route.fullPath = "/keys";
    await nextTick();
    await flushPromises();

    expect(mocks.getRegion).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it("hides and stops polling when the route becomes ineligible", async () => {
    vi.useFakeTimers();
    mocks.getRegion.mockResolvedValue({
      country_code: "CN",
      user_region_notice_enabled: true,
    });

    const wrapper = await mountAndFlush();

    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(true);
    setPublicRoute();
    await nextTick();
    await flushPromises();

    expect(wrapper.find('[data-testid="user-region-notice-overlay"]').exists()).toBe(false);
    vi.advanceTimersByTime(30_000);
    await flushPromises();
    expect(mocks.getRegion).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
