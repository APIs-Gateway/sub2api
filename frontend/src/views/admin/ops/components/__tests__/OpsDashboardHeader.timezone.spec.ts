// Adapted from upstream #7969 / 2bfc49477c0860058d2ff27d8ca40b0c3a1ca31c.
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'

vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/stores', () => ({ useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: false }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

enableAutoUnmount(afterEach)
afterEach(() => vi.useRealTimers())

async function renderHeader(now: Date) {
  // CI launches separate Node processes with explicit TZ values. Do not change
  // TZ in a worker and accidentally test a clock that retained another zone.
  const expectedZone = process.env.OPS_TIMEZONE_EXPECTED_TZ
  if (expectedZone !== undefined) expect(Intl.DateTimeFormat().resolvedOptions().timeZone).toBe(expectedZone)
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(now)
  const order: string[] = []
  const wrapper = mount(OpsDashboardHeader, {
    props: {
      platform: '', groupId: null, timeRange: '1h', queryMode: 'auto', loading: false, lastUpdated: null,
      'onUpdate:customTimeRange': () => order.push('customTimeRange'),
      'onUpdate:timeRange': () => order.push('timeRange')
    },
    global: {
      stubs: {
        Select: true, HelpTooltip: true, Icon: true,
        BaseDialog: { name: 'BaseDialog', props: ['show'], template: '<div v-if="show"><slot /></div>' }
      }
    }
  })
  await flushPromises()
  return { wrapper, order }
}

async function openCustomRange(wrapper: VueWrapper) {
  const selector = wrapper.findAllComponents({ name: 'Select' }).find(select =>
    select.props('options').some((option: { value: unknown }) => option.value === 'custom')
  )
  expect(selector).toBeDefined()
  selector!.vm.$emit('update:modelValue', 'custom')
  await flushPromises()
}

async function clickButton(wrapper: VueWrapper, label: string) {
  const button = wrapper.findAll('button').find(item => item.text() === label)
  expect(button).toBeDefined()
  await button!.trigger('click')
}

function expectSubmittedRange(wrapper: VueWrapper, order: string[], start: string, end: string) {
  expect(wrapper.emitted('update:customTimeRange')).toEqual([[start, end]])
  expect(wrapper.emitted('update:timeRange')).toEqual([['custom']])
  expect(order).toEqual(['customTimeRange', 'timeRange'])
  expect(wrapper.find('input[type="datetime-local"]').exists()).toBe(false)
}

function expectProcessLocalInputs(wrapper: VueWrapper, expected: Record<string, readonly [string, string]>) {
  const zone = process.env.OPS_TIMEZONE_EXPECTED_TZ
  if (zone !== undefined) {
    expect(expected[zone]).toBeDefined()
    expect(wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]').map(input => input.element.value))
      .toEqual(expected[zone])
  }
}

describe('Ops custom time range local clock', () => {
  it.each([
    [14, 23, '2026-10-09T13:23', '2026-10-09T14:23'],
    [0, 15, '2026-10-08T23:15', '2026-10-09T00:15']
  ] as const)('prefills the previous local hour at %i:%i', async (hour, minute, start, end) => {
    const { wrapper } = await renderHeader(new Date(2026, 9, 9, hour, minute))
    await openCustomRange(wrapper)
    const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    expect(inputs.map(input => input.element.value)).toEqual([start, end])
    expect(wrapper.emitted('update:customTimeRange')).toBeUndefined()
  })

  it('confirms the previous real hour and preserves custom-range event ordering', async () => {
    const now = new Date(2026, 9, 9, 14, 23)
    const { wrapper, order } = await renderHeader(now)
    await openCustomRange(wrapper)
    await clickButton(wrapper, 'common.confirm')
    expect(wrapper.emitted('update:customTimeRange')).toEqual([
      [new Date(now.getTime() - 3600000).toISOString(), now.toISOString()]
    ])
    expect(wrapper.emitted('update:timeRange')).toEqual([['custom']])
    expect(order).toEqual(['customTimeRange', 'timeRange'])
    expect(wrapper.find('input[type="datetime-local"]').exists()).toBe(false)
  })

  it('discards cancelled edits and initializes a reopened dialog from the new local hour', async () => {
    const { wrapper } = await renderHeader(new Date(2026, 9, 9, 14, 23))
    await openCustomRange(wrapper)
    const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    await inputs[0]!.setValue('2026-10-01T09:10')
    await inputs[1]!.setValue('2026-10-01T10:10')
    await clickButton(wrapper, 'common.cancel')
    expect(wrapper.find('input[type="datetime-local"]').exists()).toBe(false)
    expect(wrapper.emitted('update:customTimeRange')).toBeUndefined()
    expect(wrapper.emitted('update:timeRange')).toBeUndefined()

    const later = new Date(2026, 9, 9, 14, 43)
    vi.setSystemTime(later)
    await openCustomRange(wrapper)
    expect(wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]').map(input => input.element.value))
      .toEqual(['2026-10-09T13:43', '2026-10-09T14:43'])
    await clickButton(wrapper, 'common.confirm')
    expect(wrapper.emitted('update:customTimeRange')).toEqual([
      [new Date(later.getTime() - 3600000).toISOString(), later.toISOString()]
    ])
  })

  it('submits explicit local input edits as their UTC instants', async () => {
    const { wrapper, order } = await renderHeader(new Date(2026, 9, 9, 14, 23))
    await openCustomRange(wrapper)
    const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    await inputs[0]!.setValue('2026-10-08T09:10')
    await inputs[1]!.setValue('2026-10-09T14:50')
    await clickButton(wrapper, 'common.confirm')
    expect(wrapper.emitted('update:customTimeRange')).toEqual([
      [new Date(2026, 9, 8, 9, 10).toISOString(), new Date(2026, 9, 9, 14, 50).toISOString()]
    ])
    expect(order).toEqual(['customTimeRange', 'timeRange'])
  })

  it('preserves the previous elapsed hour across the autumn clock fold', async () => {
    const now = new Date('2026-11-01T06:30:00Z')
    const { wrapper } = await renderHeader(now)
    await openCustomRange(wrapper)
    if (process.env.OPS_TIMEZONE_EXPECTED_TZ === 'America/New_York') {
      expect(new Date('2026-11-01T05:30:00Z').getTimezoneOffset()).toBe(240)
      expect(now.getTimezoneOffset()).toBe(300)
      expect(wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]').map(input => input.element.value))
        .toEqual(['2026-11-01T01:30', '2026-11-01T01:30'])
    }
    await clickButton(wrapper, 'common.confirm')
    expect(wrapper.emitted('update:customTimeRange')).toEqual([
      ['2026-11-01T05:30:00.000Z', '2026-11-01T06:30:00.000Z']
    ])
  })

  it('preserves the later fold occurrence when the default hour starts after the rollback', async () => {
    const { wrapper, order } = await renderHeader(new Date('2026-11-01T07:30:00Z'))
    await openCustomRange(wrapper)
    expectProcessLocalInputs(wrapper, {
      UTC: ['2026-11-01T06:30', '2026-11-01T07:30'],
      'Asia/Shanghai': ['2026-11-01T14:30', '2026-11-01T15:30'],
      'America/New_York': ['2026-11-01T01:30', '2026-11-01T02:30']
    })
    if (process.env.OPS_TIMEZONE_EXPECTED_TZ === 'America/New_York') {
      expect(new Date('2026-11-01T05:30:00Z').getTimezoneOffset()).toBe(240)
      expect(new Date('2026-11-01T06:30:00Z').getTimezoneOffset()).toBe(300)
      expect(new Date('2026-11-01T07:30:00Z').getTimezoneOffset()).toBe(300)
    }
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, '2026-11-01T06:30:00.000Z', '2026-11-01T07:30:00.000Z')
  })

  it('keeps the unchanged end fold identity when only the start is edited', async () => {
    const { wrapper, order } = await renderHeader(new Date('2026-11-01T06:30:00Z'))
    await openCustomRange(wrapper)
    const edits: Record<string, string> = {
      UTC: '2026-11-01T05:15', 'Asia/Shanghai': '2026-11-01T13:15', 'America/New_York': '2026-11-01T01:15'
    }
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    const editedStart = edits[zone] ?? '2026-11-01T01:15'
    const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    await inputs[0]!.setValue(editedStart)
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order,
      edits[zone] ? '2026-11-01T05:15:00.000Z' : new Date(editedStart).toISOString(), '2026-11-01T06:30:00.000Z')
  })

  it('keeps the unchanged start fold identity when only the end is edited', async () => {
    const { wrapper, order } = await renderHeader(new Date('2026-11-01T07:30:00Z'))
    await openCustomRange(wrapper)
    const edits: Record<string, string> = {
      UTC: '2026-11-01T07:45', 'Asia/Shanghai': '2026-11-01T15:45', 'America/New_York': '2026-11-01T02:45'
    }
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
    const editedEnd = edits[zone] ?? '2026-11-01T02:45'
    const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    await inputs[1]!.setValue(editedEnd)
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, '2026-11-01T06:30:00.000Z',
      edits[zone] ? '2026-11-01T07:45:00.000Z' : new Date(editedEnd).toISOString())
  })

  it.each([
    ['ordinary day', '2026-10-09T14:23:59.987Z', '2026-10-09T13:23:00.000Z', '2026-10-09T14:23:00.000Z', {
      UTC: ['2026-10-09T13:23', '2026-10-09T14:23'],
      'Asia/Shanghai': ['2026-10-09T21:23', '2026-10-09T22:23'],
      'America/New_York': ['2026-10-09T09:23', '2026-10-09T10:23']
    }],
    ['clock fold', '2026-11-01T06:30:59.987Z', '2026-11-01T05:30:00.000Z', '2026-11-01T06:30:00.000Z', {
      UTC: ['2026-11-01T05:30', '2026-11-01T06:30'],
      'Asia/Shanghai': ['2026-11-01T13:30', '2026-11-01T14:30'],
      'America/New_York': ['2026-11-01T01:30', '2026-11-01T01:30']
    }]
  ] as const)('floors default seconds and milliseconds on %s', async (_label, now, start, end, localInputs) => {
    const { wrapper, order } = await renderHeader(new Date(now))
    await openCustomRange(wrapper)
    expectProcessLocalInputs(wrapper, localInputs)
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, start, end)
  })

  it.each(['cancel button', 'dialog close'] as const)('replaces the cancelled fold snapshot on reopen through %s', async (closeMethod) => {
    const { wrapper, order } = await renderHeader(new Date('2026-11-01T06:30:00Z'))
    await openCustomRange(wrapper)
    await wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')[0]!.setValue('2026-10-01T09:10')
    if (closeMethod === 'cancel button') {
      await clickButton(wrapper, 'common.cancel')
    } else {
      const dialogs = wrapper.findAllComponents({ name: 'BaseDialog' }).filter(dialog =>
        dialog.find('input[type="datetime-local"]').exists()
      )
      expect(dialogs).toHaveLength(1)
      dialogs[0]!.vm.$emit('close')
      await flushPromises()
    }
    expect(wrapper.emitted('update:customTimeRange')).toBeUndefined()
    expect(wrapper.emitted('update:timeRange')).toBeUndefined()
    expect(wrapper.find('input[type="datetime-local"]').exists()).toBe(false)
    vi.setSystemTime(new Date('2026-11-01T07:30:00Z'))
    await openCustomRange(wrapper)
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, '2026-11-01T06:30:00.000Z', '2026-11-01T07:30:00.000Z')
  })

  it('keeps the opening minute snapshot when the clock advances before confirmation', async () => {
    const { wrapper, order } = await renderHeader(new Date('2026-10-09T14:23:59.987Z'))
    await openCustomRange(wrapper)
    vi.setSystemTime(new Date('2026-10-09T15:45:12.345Z'))
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, '2026-10-09T13:23:00.000Z', '2026-10-09T14:23:00.000Z')
  })

  it.each([['start', 0], ['end', 1]] as const)('keeps the dialog and snapshot when the %s endpoint is empty', async (_label, index) => {
    const { wrapper, order } = await renderHeader(new Date('2026-11-01T07:30:00Z'))
    await openCustomRange(wrapper)
    const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    const initial = inputs[index]!.element.value
    await inputs[index]!.setValue('')
    await clickButton(wrapper, 'common.confirm')
    expect(wrapper.emitted('update:customTimeRange')).toBeUndefined()
    expect(wrapper.emitted('update:timeRange')).toBeUndefined()
    expect(wrapper.find('input[type="datetime-local"]').exists()).toBe(true)
    await inputs[index]!.setValue(initial)
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, '2026-11-01T06:30:00.000Z', '2026-11-01T07:30:00.000Z')
  })

  it('restores the captured fold identity after an edit returns to the displayed default', async () => {
    const { wrapper, order } = await renderHeader(new Date('2026-11-01T07:30:00Z'))
    await openCustomRange(wrapper)
    const input = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')[0]!
    const initial = input.element.value
    await input.setValue('2026-10-01T09:10')
    await input.setValue(initial)
    await clickButton(wrapper, 'common.confirm')
    expectSubmittedRange(wrapper, order, '2026-11-01T06:30:00.000Z', '2026-11-01T07:30:00.000Z')
  })
})
