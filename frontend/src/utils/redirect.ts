/**
 * 登录 / 注册链路里的 `redirect`（完成后回到哪里）校验。
 *
 * 全站只认这一处：凡是读到 redirect 并据此跳转、或把它继续传给下一步（注册、邮箱验证、
 * 登录、2FA、第三方登录的发起与回调）的地方，都必须先过 `sanitizeRedirectPath`，
 * 不要再各写各的判断。
 *
 * 只接受站内路径。任何一条不满足就整体拒绝（调用方回落到默认页），不做"修剪后放行"：
 * - 必须以单个 `/` 开头：第二个字符不能是 `/` 或 `\`（`//host`、`/\host` 浏览器都会当成别的站点）。
 * - 不含反斜杠、空白和控制字符（浏览器会吞掉 Tab / 换行，`/<Tab>/host` 会变成 `//host`）。
 * - 不含 `://`（协议前缀，包括夹在路径或查询里的）。
 * - 百分号编码不能绕过上面几条：路径部分反复解码到稳定后再查一遍
 *   （`/%2F%2Fhost`、`/%5Chost`、`/%252F%252Fhost`），残缺的编码（`%zz`）直接拒绝。
 * - 路径里不能有 `.` / `..` 段：`/.//host` 经浏览器规整后会变成 `//host`，正常的 redirect 也不会这样写。
 * - 不能指回 `/login`、`/register` 本身：已登录用户会被导回 redirect，指回认证入口只会多绕一圈。
 *
 * 查询串和 hash 里的内容当作数据，不要求为站内路径；真正会拿它跳转的页面（例如微信绑定
 * 回调里再嵌一层 redirect）自己再过一遍本函数。
 */

export const DEFAULT_REDIRECT_PATH = '/dashboard'

const MAX_REDIRECT_LENGTH = 2048
// 路径反复解码的上限；还没稳定说明嵌套得不正常，直接拒绝
const MAX_DECODE_ROUNDS = 5

// 原始字符串里不允许出现的字符：所有空白（含空格、NBSP、行/段分隔符）、C0/C1 控制字符、BOM
// eslint-disable-next-line no-control-regex
const RAW_FORBIDDEN = /[\s\u0000-\u001f\u007f-\u009f\ufeff]/
// 解码之后不允许出现的字符：同上，但允许普通空格（`/a%20b` 是合法路径）
// eslint-disable-next-line no-control-regex
const DECODED_FORBIDDEN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\ufeff]/

// 登录成功后不该再回到的认证入口
const AUTH_ENTRY_PATHS = new Set(['/login', '/register'])

function hasSafeSlashPrefix(value: string): boolean {
  return value.charCodeAt(0) === 47 /* / */ && value[1] !== '/' && value[1] !== '\\'
}

function tryDecode(value: string): string | null {
  try {
    return decodeURIComponent(value)
  } catch {
    return null
  }
}

export function isSafeRedirectPath(value: unknown): value is string {
  if (typeof value !== 'string' || value.length === 0 || value.length > MAX_REDIRECT_LENGTH) {
    return false
  }
  if (RAW_FORBIDDEN.test(value) || !hasSafeSlashPrefix(value)) {
    return false
  }

  // 整串解码一次：编码过的反斜杠、控制字符、协议前缀都不放行
  const decodedOnce = tryDecode(value)
  if (decodedOnce === null) return false
  if (decodedOnce.includes('\\') || decodedOnce.includes('://') || DECODED_FORBIDDEN.test(decodedOnce)) {
    return false
  }

  // 路径部分（`?`、`#` 之前）反复解码到稳定，每一层都要仍是站内路径
  let path = value.split(/[?#]/, 1)[0]
  for (let round = 0; ; round++) {
    if (round >= MAX_DECODE_ROUNDS) return false
    if (!hasSafeSlashPrefix(path) || path.includes('\\') || DECODED_FORBIDDEN.test(path)) {
      return false
    }
    if (path.split('/').some((segment) => segment === '.' || segment === '..')) {
      return false
    }
    const next = tryDecode(path)
    if (next === null) return false
    if (next === path) break
    path = next
  }

  const normalized = path.toLowerCase().replace(/\/+$/, '')
  return !AUTH_ENTRY_PATHS.has(normalized)
}

/**
 * 返回可以放心跳转的站内路径；不合法（缺省、数组、外站、绕过写法）一律返回 `fallback`。
 */
export function sanitizeRedirectPath(value: unknown, fallback: string = DEFAULT_REDIRECT_PATH): string {
  return isSafeRedirectPath(value) ? value : fallback
}

/**
 * 把 redirect 带到下一步页面的 query；缺省或不合法时返回空对象（下一步就用默认页）。
 * 例：`<router-link :to="{ path: '/register', query: redirectQuery(route.query.redirect) }">`
 */
export function redirectQuery(value: unknown): { redirect?: string } {
  return isSafeRedirectPath(value) ? { redirect: value } : {}
}
