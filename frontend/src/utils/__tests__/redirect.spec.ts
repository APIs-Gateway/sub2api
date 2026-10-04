import { describe, expect, it } from 'vitest'
import {
  DEFAULT_REDIRECT_PATH,
  isSafeRedirectPath,
  redirectQuery,
  sanitizeRedirectPath
} from '@/utils/redirect'

describe('isSafeRedirectPath：放行站内路径', () => {
  it.each([
    '/',
    '/keys',
    '/keys?new=1',
    '/keys#create',
    '/dashboard',
    '/admin/users',
    '/usage?model=gpt-5&range=7d',
    '/docs/zh-CN/quickstart',
    '/custom/abc-123',
    '/中文路径?关键字=值',
    // 合法编码：编码斜杠和空格放在查询值里、空格放在路径里
    '/usage?model=a%2Fb',
    '/my%20page',
    '/a%2Fb',
    // 微信「绑定已有账号」登录后回到回调页，回调页自己带了一层 redirect
    '/auth/wechat/callback?wechat_bind_existing=1&redirect=%2Fdashboard&mode=open',
    // 登录页不是目标，但名字相近的路径是
    '/login-help',
    '/registered'
  ])('放行 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(true)
    expect(sanitizeRedirectPath(value)).toBe(value)
  })
})

describe('isSafeRedirectPath：拒绝非字符串和空值', () => {
  it.each([
    ['undefined', undefined],
    ['null', null],
    ['空串', ''],
    ['数字', 42],
    ['对象', { path: '/keys' }],
    ['数组（redirect 重复出现）', ['/keys', '/usage']],
    ['布尔', true]
  ])('拒绝 %s', (_name, value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
    expect(sanitizeRedirectPath(value)).toBe(DEFAULT_REDIRECT_PATH)
  })
})

describe('isSafeRedirectPath：拒绝不是站内绝对路径的写法', () => {
  it.each([
    'keys',
    './keys',
    '../keys',
    '?next=/keys',
    '#/keys',
    '.evil.com',
    'evil.com',
    'evil.com/keys'
  ])('拒绝相对写法 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })
})

describe('isSafeRedirectPath：拒绝协议相对与外站地址', () => {
  it.each([
    '//evil.com',
    '//evil.com/keys',
    '///evil.com',
    '////evil.com',
    '//',
    '/\\evil.com',
    '/\\\\evil.com',
    '\\\\evil.com',
    '\\/evil.com',
    '/\\/evil.com',
    '/keys\\..\\evil',
    '/keys?x=\\evil.com'
  ])('拒绝 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })
})

describe('isSafeRedirectPath：拒绝协议前缀', () => {
  it.each([
    'http://evil.com',
    'https://evil.com',
    'HTTPS://evil.com',
    'ftp://evil.com',
    'javascript:alert(1)',
    'JaVaScRiPt:alert(1)',
    'javascript://%0aalert(1)',
    'data:text/html,<script>alert(1)</script>',
    'vbscript:msgbox(1)',
    'file:///etc/passwd',
    'mailto:a@b.com',
    // 夹在路径或查询里的协议前缀
    '/redirect?u=http://evil.com',
    '/x/https://evil.com',
    // 编码后的协议前缀
    '/redirect?u=http%3A%2F%2Fevil.com',
    '/redirect?u=https%3a%2f%2fevil.com'
  ])('拒绝 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })
})

describe('isSafeRedirectPath：拒绝空白与控制字符（浏览器会吞掉 Tab 和换行）', () => {
  it.each([
    ' /keys',
    '/keys ',
    ' //evil.com',
    '\t/keys',
    '\n/keys',
    '\r/keys',
    '/\t/evil.com',
    '/\n/evil.com',
    '/\r/evil.com',
    '/\r\nSet-Cookie: x=1',
    '/keys\u0000',
    '/keys\u007f',
    '/\u0085/evil.com',
    '/ /evil.com',
    '/\u2028/evil.com',
    '/\u2029/evil.com',
    '/\ufeff/evil.com',
    '/a b'
  ])('拒绝 %j', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })
})

describe('isSafeRedirectPath：拒绝百分号编码绕过', () => {
  it.each([
    // 编码的斜杠、反斜杠
    '/%2Fevil.com',
    '/%2F%2Fevil.com',
    '/%2f/evil.com',
    '/%2F/evil.com',
    '/%5Cevil.com',
    '/%5cevil.com',
    '/%2F%5Cevil.com',
    '/keys%5C..%5Cevil',
    // 编码的 Tab / 换行 / 回车 / 空字符
    '/%09/evil.com',
    '/%0a/evil.com',
    '/%0A/evil.com',
    '/%0d/evil.com',
    '/%00',
    '/keys?x=%0d%0aSet-Cookie:%20a=b',
    // 多重编码
    '/%252F%252Fevil.com',
    '/%255Cevil.com',
    '/%25252Fevil.com',
    '/%2525252Fevil.com',
    '/%252F%255Cevil.com',
    // 整串被编码：不以 / 开头
    '%2F%2Fevil.com',
    '%2Fkeys',
    // 残缺的编码
    '/%',
    '/%zz',
    '/%E0%A4%A',
    '/keys?x=100%'
  ])('拒绝 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })
})

describe('isSafeRedirectPath：拒绝路径里的 . 和 .. 段（规整后会变成 //host）', () => {
  it.each([
    '/.//evil.com',
    '/./keys',
    '/../keys',
    '/a/../b',
    '/a/./b',
    '/keys/..',
    '/keys/.',
    '/%2e%2e/keys',
    '/%2E/keys',
    '/a/%2e%2E/b',
    '/.%2e/keys',
    '/%252e%252e/keys'
  ])('拒绝 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })

  it('名字里带点的段不受影响', () => {
    expect(isSafeRedirectPath('/docs/v1.2/intro')).toBe(true)
    expect(isSafeRedirectPath('/.well-known/x')).toBe(true)
    expect(isSafeRedirectPath('/keys?back=../x')).toBe(true)
  })
})

describe('isSafeRedirectPath：不回到认证入口，避免绕圈', () => {
  it.each([
    '/login',
    '/login/',
    '/login?redirect=/keys',
    '/login#x',
    '/Login',
    '/LOGIN',
    '/register',
    '/register?redirect=/keys',
    '/REGISTER/',
    // 编码过的 login
    '/%6Cogin',
    '/%256Cogin'
  ])('拒绝 %s', (value) => {
    expect(isSafeRedirectPath(value)).toBe(false)
  })
})

describe('isSafeRedirectPath：长度上限', () => {
  it('2048 个字符以内放行', () => {
    const value = '/keys?q=' + 'a'.repeat(2048 - '/keys?q='.length)
    expect(value).toHaveLength(2048)
    expect(isSafeRedirectPath(value)).toBe(true)
  })

  it('超过 2048 个字符拒绝', () => {
    expect(isSafeRedirectPath('/keys?q=' + 'a'.repeat(2048))).toBe(false)
  })
})

describe('isSafeRedirectPath：放行的值解析后一定还在本站（穷举危险字符的组合）', () => {
  const base = 'https://app.example.test'
  // 反斜杠、斜杠、各种编码、Tab、冒号、点、@、查询号都是常见的绕过材料
  const tokens = ['/', '\\', '%2F', '%5C', '%09', '%25', ':', '.', 'a', '?', '\t', '@']

  function* combinations(maxLength: number): Generator<string> {
    let layer = ['']
    for (let length = 1; length <= maxLength; length++) {
      const next: string[] = []
      for (const prefix of layer) {
        for (const token of tokens) {
          const candidate = prefix + token
          next.push(candidate)
          yield candidate
        }
      }
      layer = next
    }
  }

  it('任何被放行的组合，用浏览器同款 URL 解析后 origin 不变且路径不以 // 开头', () => {
    let accepted = 0
    let checked = 0
    for (const tail of combinations(5)) {
      for (const candidate of [tail, '/' + tail]) {
        checked++
        if (!isSafeRedirectPath(candidate)) continue
        accepted++
        const resolved = new URL(candidate, base)
        expect(resolved.origin, JSON.stringify(candidate)).toBe(base)
        expect(resolved.pathname.startsWith('//'), JSON.stringify(candidate)).toBe(false)
      }
    }
    // 防止校验函数把所有东西都拒绝掉而让这个测试空转
    expect(accepted).toBeGreaterThan(1000)
    expect(checked).toBeGreaterThan(400000)
  })
})

describe('sanitizeRedirectPath', () => {
  it('不合法时回落到默认页 /dashboard', () => {
    expect(sanitizeRedirectPath('//evil.com')).toBe('/dashboard')
    expect(sanitizeRedirectPath('/\\evil.com')).toBe('/dashboard')
    expect(sanitizeRedirectPath(undefined)).toBe('/dashboard')
  })

  it('可以指定回落地址', () => {
    expect(sanitizeRedirectPath('//evil.com', '/purchase')).toBe('/purchase')
    expect(sanitizeRedirectPath('/keys', '/purchase')).toBe('/keys')
  })

  it('原样返回合法值，不做改写', () => {
    expect(sanitizeRedirectPath('/keys?new=1#top')).toBe('/keys?new=1#top')
  })
})

describe('redirectQuery', () => {
  it('合法值原样带到下一步', () => {
    expect(redirectQuery('/keys')).toEqual({ redirect: '/keys' })
  })

  it('缺省或不合法时不带，下一步用默认页', () => {
    expect(redirectQuery(undefined)).toEqual({})
    expect(redirectQuery('')).toEqual({})
    expect(redirectQuery('//evil.com')).toEqual({})
    expect(redirectQuery(['/keys'])).toEqual({})
  })
})
