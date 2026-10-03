import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

// 删除分组不再连带删订阅卡，也不再区分分组类型：所有分组共用同一条确认文案，
// 且不能再出现「删除订阅记录」「密钥不再属于任何分组」这类与实际行为不符的说法。
const locales = {
  en: {
    groups: en.admin.groups as Record<string, unknown>,
    cardsUntouched: 'Existing subscription cards are not affected',
    staleClaims: ['no longer belong to any group', 'delete all related subscription records']
  },
  'zh-CN': {
    groups: zhCN.admin.groups as Record<string, unknown>,
    cardsUntouched: '订阅卡不受影响',
    staleClaims: ['不再属于任何分组', '删除所有相关的订阅记录']
  },
  'zh-HK': {
    groups: zhHK.admin.groups as Record<string, unknown>,
    cardsUntouched: '訂閱卡不受影響',
    staleClaims: ['不再屬於任何分組', '刪除所有相關的訂閱記錄']
  }
}

describe('admin group delete confirmation copy', () => {
  for (const [name, locale] of Object.entries(locales)) {
    it(`${name}: tells the admin what happens and keeps subscription cards`, () => {
      const message = locale.groups.deleteConfirm
      expect(typeof message).toBe('string')
      expect(message).toContain('{name}')
      expect(message).toContain(locale.cardsUntouched)
      for (const stale of locale.staleClaims) {
        expect(message).not.toContain(stale)
      }
    })

    it(`${name}: no longer has a separate subscription-group message`, () => {
      expect(locale.groups.deleteConfirmSubscription).toBeUndefined()
    })
  }

  it('zh-HK uses the 金鑰 wording', () => {
    expect(zhHK.admin.groups.deleteConfirm).toContain('金鑰')
    expect(zhHK.admin.groups.deleteConfirm).not.toContain('密鑰')
  })
})
