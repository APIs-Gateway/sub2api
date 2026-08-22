import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";
import SubscriptionPlanCard from "../SubscriptionPlanCard.vue";

const i18n = createI18n({
  legacy: false,
  locale: "en",
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    en: {
      payment: {
        days: "days",
        models: "Models",
        planCard: {
          quota: "Quota",
          rate: "Rate",
          unlimited: "Unlimited",
        },
        subscribeNow: "Subscribe now",
      },
    },
  },
});

const mountPlanCard = (groupPlatform: string) =>
  mount(SubscriptionPlanCard, {
    props: {
      plan: {
        id: 1,
        group_id: 10,
        group_platform: groupPlatform,
        name: "Pro",
        price: 10,
        amount: 1000,
        features: [],
        rate_multiplier: 1,
        validity_days: 30,
        validity_unit: "day",
        supported_model_scopes: ["claude", "gemini_text", "gemini_image"],
        is_active: true,
      },
    },
    global: { plugins: [i18n] },
  });

describe("SubscriptionPlanCard", () => {
  it("shows the optional display currency beside current and original prices", () => {
    const wrapper = mount(SubscriptionPlanCard, {
      props: {
        plan: {
          id: 1,
          group_id: 10,
          group_platform: "openai",
          name: "Pro",
          price: 10,
          original_price: 20,
          currency: "USD",
          features: [],
          rate_multiplier: 1,
          validity_days: 30,
          validity_unit: "day",
        },
      },
      global: { plugins: [i18n] },
    })

    expect(wrapper.text()).toContain("USD")
  })

  it("does not show Antigravity model scopes for OpenAI plans", () => {
    const text = mountPlanCard("openai").text();

    expect(text).not.toContain("Claude");
    expect(text).not.toContain("Gemini");
    expect(text).not.toContain("Imagen");
  });

  it("shows model scopes for Antigravity plans", () => {
    const text = mountPlanCard("antigravity").text();

    expect(text).toContain("Claude");
    expect(text).toContain("Gemini");
    expect(text).toContain("Imagen");
  });

  const mountPlanCardWithName = (name: string) =>
    mount(SubscriptionPlanCard, {
      props: {
        plan: {
          id: 1,
          group_id: 10,
          group_platform: "openai",
          name,
          price: 10,
          features: [],
          rate_multiplier: 1,
          validity_days: 30,
          validity_unit: "day",
        },
      },
      global: { plugins: [i18n] },
    });

  it.each([
    ["long Chinese", "企业全球加速专业订阅套餐（含高级模型与优先支持）"],
    ["long English", "Enterprise Global Acceleration Subscription with Priority Support"],
    ["unbroken token", "EnterpriseGlobalAccelerationSubscriptionWithPrioritySupport1234567890"],
  ])("keeps the full %s plan title accessible in a bounded two-line area", (_label, name) => {
    const wrapper = mountPlanCardWithName(name);
    const title = wrapper.get("h3");

    expect(title.text()).toBe(name);
    expect(title.attributes("title")).toBe(name);
    expect(title.classes()).toEqual(expect.arrayContaining([
      "min-w-0",
      "h-12",
      "break-words",
      "line-clamp-2",
      "[overflow-wrap:anywhere]",
    ]));
    expect(title.classes()).not.toContain("truncate");
  });

  it("keeps title, badge, and validity suffix in separate bounded regions", () => {
    const wrapper = mountPlanCardWithName("Enterprise Global Acceleration Subscription with Priority Support");
    const title = wrapper.get("h3");
    const badge = wrapper.findAll("span").find((node) => node.text() === "OpenAI");

    expect(title.element.parentElement?.classList).toContain("min-w-0");
    expect(title.element.parentElement?.classList).toContain("flex-1");
    expect(badge?.classes()).toContain("shrink-0");
    expect([...(badge?.element.parentElement?.classList ?? [])]).toEqual(expect.arrayContaining([
      "flex",
      "items-center",
      "justify-end",
    ]));
    // Assert the structural relationship (badge and validity suffix share one row) instead of
    // the translated text: vitest aliases vue-i18n to the runtime-only build, so t() here only
    // ever echoes the message key back (e.g. "payment.days"), not the actual translation.
    const suffixSpan = badge?.element.nextElementSibling;
    expect(suffixSpan?.tagName).toBe("SPAN");
    expect(suffixSpan?.textContent?.trim().startsWith("/")).toBe(true);
  });
});
