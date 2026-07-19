import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamBillingRateCell from '../UpstreamBillingRateCell.vue'
import type { Account, UpstreamBillingProbeSnapshot } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${Object.values(params).join(',')}` : key
  })
}))

const makeAccount = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'upstream',
  platform: 'openai',
  type: 'apikey',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-07-13T00:00:00Z',
  updated_at: '2026-07-13T00:00:00Z',
  schedulable: true,
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  ...overrides
})

const billingData = {
  object: 'sub2api.key_billing' as const,
  schema_version: 1 as const,
  billing_scope: 'token' as const,
  group_rate_multiplier: 0.8,
  resolved_rate_multiplier: 0.6,
  peak_rate_enabled: true,
  peak_start: '09:00',
  peak_end: '18:00',
  peak_rate_multiplier: 1.5,
  applied_peak_multiplier: 1.5,
  effective_rate_multiplier: 0.9,
  timezone: 'Asia/Shanghai',
  observed_at: '2026-07-13T00:00:00Z'
}

const snapshot = (overrides: Partial<UpstreamBillingProbeSnapshot> = {}): UpstreamBillingProbeSnapshot => ({
  status: 'ok' as const,
  data: billingData,
  received_at: '2026-07-13T00:00:00Z',
  fresh_until: '2026-07-14T00:00:00Z',
  last_attempt_at: '2026-07-13T00:00:00Z',
  next_probe_at: '2026-07-13T00:30:00Z',
  ...overrides
})

const mountCell = (account: Account, now = Date.parse('2026-07-13T00:30:00Z'), globalProbeEnabled = true) =>
  mount(UpstreamBillingRateCell, {
    props: { account, now, globalProbeEnabled },
    global: {
      stubs: {
        HelpTooltip: {
          inheritAttrs: false,
          template: '<div v-bind="$attrs"><slot name="trigger" /><slot /></div>'
        },
        Icon: true
      }
    }
  })

describe('UpstreamBillingRateCell', () => {
  it('shows the base rate off peak and the peak-adjusted rate in the window', async () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe_enabled: true,
      upstream_billing_probe: snapshot()
    }}))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.6x')
    await wrapper.setProps({ now: Date.parse('2026-07-13T01:00:00Z') })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.9x')
    expect(wrapper.get('[data-testid="upstream-billing-probe"]')).toBeTruthy()
  })

  it('keeps the last detected value visible when the snapshot is stale', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe: snapshot({ fresh_until: '2026-07-12T23:00:00Z' })
    }}))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.lastDetectedRate:0.9')
  })

  it('shows failure and global-off states without exposing a next probe time', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe_enabled: true,
      upstream_billing_probe: snapshot({
        status: 'failed',
        data: undefined,
        fresh_until: undefined,
        last_error: 'network_error'
      })
    }}), Date.parse('2026-07-13T00:30:00Z'), false)

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.failed')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.globalProbeState')
    expect(wrapper.find('[data-testid="upstream-billing-next-probe"]').exists()).toBe(false)
  })

  it('does not render the probe control for non-OpenAI API key accounts and emits for eligible ones', async () => {
    const wrapper = mountCell(makeAccount())
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)

    await wrapper.setProps({ account: makeAccount({ type: 'oauth' }) })
    expect(wrapper.text()).toBe('-')
    expect(wrapper.find('[data-testid="upstream-billing-probe"]').exists()).toBe(false)
  })

  it('fails closed for malformed billing data and timestamps', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe: snapshot({
        data: { ...billingData, resolved_rate_multiplier: -1 },
        received_at: 'not-a-time'
      })
    }}))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
  })

  it('covers non-peak and relative-time presentation branches', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe: snapshot({
        data: { ...billingData, peak_rate_enabled: false },
        received_at: '2026-07-12T22:00:00Z',
        fresh_until: '2026-07-12T23:00:00Z'
      })
    }}), Date.parse('2026-07-13T00:30:00Z'))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.hoursAgo:2')
  })

  it('shows unsupported snapshots and keeps a valid next-probe timestamp', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe_enabled: true,
      upstream_billing_probe: snapshot({
        status: 'unsupported',
        data: undefined,
        fresh_until: undefined
      })
    }}))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.unsupported')
    expect(wrapper.get('[data-testid="upstream-billing-next-probe"]')).toBeTruthy()
  })

  it('rejects invalid time zones and malformed peak windows', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe: snapshot({
        data: {
          ...billingData,
          timezone: 'Invalid/Zone',
          peak_start: '25:00',
          peak_end: 'bad'
        }
      })
    }}))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('-')
  })

  it('uses the fallback freshness window and formats elapsed days', () => {
    const wrapper = mountCell(makeAccount({ extra: {
      upstream_billing_probe: snapshot({
        received_at: '2026-07-10T00:00:00Z',
        fresh_until: undefined
      })
    }}), Date.parse('2026-07-13T00:30:00Z'))

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.daysAgo:3')
  })
})
