/**
 * Single declaration of the user-side navigation: every entry, its label and
 * the gates that hide it.
 *
 * - The order of USER_NAV_ENTRIES is the display order. The user sidebar and the
 *   admin "My account" section both render this list as-is (the latter without
 *   `userOnly` entries), so reordering it changes both menus.
 * - Gating is declared here (feature flag, simple mode) and applied by the
 *   consumer, so the sidebar and any future consumer (command palette, ...)
 *   hide exactly the same entries.
 * - Custom menu items configured by the site admin are not part of this list;
 *   the consumer appends them after the last entry.
 */

import { FeatureFlags, type FeatureFlagDefinition } from '@/utils/featureFlags'

export interface UserNavEntry {
  path: string
  /** i18n key of the entry label. */
  labelKey: string
  hideInSimpleMode?: boolean
  /** Registered public-settings flag; the entry is hidden when it resolves to false. */
  flag?: FeatureFlagDefinition
  /** Shown in the user sidebar only (the admin personal section has its own dashboard link). */
  userOnly?: boolean
}

export const USER_NAV_ENTRIES: readonly UserNavEntry[] = [
  { path: '/dashboard', labelKey: 'nav.dashboard', userOnly: true },
  { path: '/keys', labelKey: 'nav.apiKeys' },
  { path: '/usage', labelKey: 'nav.usage', hideInSimpleMode: true },
  {
    path: '/available-channels',
    labelKey: 'nav.availableChannels',
    hideInSimpleMode: true,
    flag: FeatureFlags.availableChannels,
  },
  { path: '/monitor', labelKey: 'nav.channelStatus', flag: FeatureFlags.channelMonitor },
  { path: '/subscriptions', labelKey: 'nav.mySubscriptions', hideInSimpleMode: true },
  {
    path: '/purchase',
    labelKey: 'nav.buySubscription',
    hideInSimpleMode: true,
    flag: FeatureFlags.payment,
  },
  {
    path: '/orders',
    labelKey: 'nav.myOrders',
    hideInSimpleMode: true,
    flag: FeatureFlags.payment,
  },
  { path: '/redeem', labelKey: 'nav.redeem', hideInSimpleMode: true },
  { path: '/points', labelKey: 'nav.points', hideInSimpleMode: true },
  { path: '/docs', labelKey: 'nav.usageDocs' },
  { path: '/profile', labelKey: 'nav.profile' },
]
