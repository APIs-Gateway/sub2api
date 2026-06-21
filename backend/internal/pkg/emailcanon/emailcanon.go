// Package emailcanon 提供邮箱归一化（含 Gmail 别名过滤）的低层纯函数与一个进程内开关。
//
// 放在低层 pkg 是为了让 repository 与 service 两层共用同一套归一化逻辑、且 repository 不必
// 依赖 service（避免循环）。Gmail 把本地部分的「.」忽略、把首个「+」后的内容当作 tag 丢弃，
// 且 googlemail.com 与 gmail.com 等价——因此 f.o.o+tag@gmail.com、foo@googlemail.com 与
// foo@gmail.com 收件箱相同，常被用同一邮箱注册多个账号薅订阅。开启过滤后这些别名归一化为
// 同一规范地址，配合查重/唯一约束阻止重复注册。
//
// 开关由 SettingService 在启动预热与设置更新时通过 SetEnabled 同步（见 setting_service.go
// 的 refreshCachedSettings 与启动装配）。关闭时 CanonicalizeEmail 退化为 ToLower+TrimSpace，
// 与历史行为完全一致。
package emailcanon

import (
	"strings"
	"sync/atomic"
)

// enabled 控制是否启用 Gmail 别名过滤。默认开启（feature 默认值），由 SettingService 同步覆盖。
var enabled atomic.Bool

func init() { enabled.Store(true) }

// SetEnabled 设置 Gmail 别名过滤开关（由 SettingService 在启动/更新时调用）。
func SetEnabled(v bool) { enabled.Store(v) }

// Enabled 报告 Gmail 别名过滤当前是否开启。
func Enabled() bool { return enabled.Load() }

// CanonicalizeEmail 返回邮箱的规范化形式：先 ToLower+TrimSpace（始终执行，与历史一致）；
// 若过滤开启且域名为 gmail.com / googlemail.com，则丢弃本地部分首个「+」后的 tag、移除所有
// 「.」，并把域名统一为 gmail.com。其他域名只做 ToLower+TrimSpace（点号在多数服务商有意义，
// 不能乱去）。无法解析或归一化后本地部分为空时按原样（lower+trim）返回，绝不产出「@gmail.com」。
func CanonicalizeEmail(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if !enabled.Load() {
		return e
	}
	at := strings.LastIndexByte(e, '@')
	if at <= 0 || at == len(e)-1 {
		return e // 无本地部分或无域名，原样返回
	}
	local, domain := e[:at], e[at+1:]
	if domain != "gmail.com" && domain != "googlemail.com" {
		return e
	}
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus] // 丢弃 +tag
	}
	local = strings.ReplaceAll(local, ".", "") // 去掉所有点
	if local == "" {
		return e // 退化地址（如 "...."@gmail.com / "+tag"@gmail.com），不归一化
	}
	return local + "@gmail.com"
}

// CanonicalizeEmailForStorage 返回落库用的邮箱：仅当过滤开启且为 Gmail/googlemail 别名时归一化为
// 规范 Gmail 地址；其余情况（关闭、非 Gmail、无法解析）原样返回，保留历史「原值落库」行为
// （非 Gmail 的大小写/空格去重仍由查询端 LOWER(TRIM) 负责，无需改动存储值）。
func CanonicalizeEmailForStorage(email string) string {
	if !enabled.Load() {
		return email
	}
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(e, '@')
	if at <= 0 || at == len(e)-1 {
		return email
	}
	domain := e[at+1:]
	if domain != "gmail.com" && domain != "googlemail.com" {
		return email // 非 Gmail：原样落库
	}
	if canon := CanonicalizeEmail(email); canon != "" {
		return canon
	}
	return email
}
