package service

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// clientModelView：free 站「用户只看到自己请求的模型名」的改写库。
//
// 与 alignClientVisibleModel（只改顶层 model / response.model）不同，这里同时做两件事：
//   - 字段级：任意深度的 model / *model / *modelid / *modelname 字符串字段统一换成 client（A）；
//   - 文本级：其余字符串叶子（错误文案、状态文案等）里出现的外来模型名（C 及其变体）换成 A。
//
// 界线：只改发给客户端的字节。模型正文、推理、工具参数/输出、instructions/tools/input 回显、
// encrypted_content、*faster_model 一律不碰；审计侧必须在调用本库之前读走上游原始值。

const clientModelViewContextKey = "client_model_view"

const (
	clientModelViewMaxDepth = 64 // JSON 嵌套深度上限，超出的子树不处理
	clientModelViewMaxNest  = 3  // 「字符串里的 JSON」递归层数上限
)

// clientModelViewSkipKeys 是任意深度整棵子树都不碰的键（compact 形式）。
var clientModelViewSkipKeys = map[string]struct{}{
	"output": {}, "outputtext": {}, "content": {}, "contentblock": {}, "delta": {}, "item": {},
	"part": {}, "choices": {}, "instructions": {}, "tools": {}, "input": {}, "text": {},
	"arguments": {}, "refusal": {}, "reasoning": {}, "summary": {}, "encryptedcontent": {},
	// 代码、转写、日志概率、图片 base64、工具结果、引用与推理文本：都是模型正文或二进制，不碰。
	"code": {}, "transcript": {}, "logprobs": {}, "partialimageb64": {}, "b64json": {}, "result": {},
	"annotation": {}, "annotations": {}, "reasoningcontent": {}, "reasoningtext": {},
}

// clientModelVariant 是外来名的一个写法，已切成小写的字母段/数字段。
type clientModelVariant struct {
	text       string
	segs       []string
	prefixOnly bool // 与 A 完全相等：只有前面紧邻被吞掉的 `xx/` 或 `xx:` 前缀时才命中
}

type clientModelView struct {
	client  string               // A：用户请求的模型名
	names   []string             // 外来名原始输入（去空去重），withForeign 用
	foreign []clientModelVariant // 外来名及其变体，长者优先
}

// newClientModelView 构造视图；client 为空（含纯空白）→ false。
func newClientModelView(client string, foreign ...string) (clientModelView, bool) {
	client = strings.TrimSpace(client)
	if client == "" {
		return clientModelView{}, false
	}
	return clientModelView{client: client}.withForeign(foreign...), true
}

// withForeign 返回追加了外来名的新视图，不修改接收者。
func (v clientModelView) withForeign(names ...string) clientModelView {
	if v.client == "" || len(names) == 0 {
		return v
	}
	merged := make([]string, 0, len(v.names)+len(names))
	seen := make(map[string]struct{}, len(v.names)+len(names))
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" {
			return
		}
		if _, dup := seen[n]; dup {
			return
		}
		seen[n] = struct{}{}
		merged = append(merged, n)
	}
	for _, n := range v.names {
		add(n)
	}
	for _, n := range names {
		add(n)
	}
	if len(merged) == len(v.names) {
		return v
	}
	return clientModelView{client: v.client, names: merged, foreign: buildClientModelVariants(v.client, merged)}
}

// buildClientModelVariants 由外来名生成匹配变体：
//   - 与 A 相等（不区分大小写）的外来名本身不产生任何变体（恒等映射 / 上游回显 A，没有要换的东西）；
//   - 每个变体都要过 clientModelPlausibleName，拆出来的尾巴（`auto`、`default`、`latest`）不能变成全文匹配的普通词；
//   - 变体与 A 相等时（只会来自本身带前缀的外来名，如 `cn:glm-5.3-flash`）标成 prefixOnly。
func buildClientModelVariants(client string, names []string) []clientModelVariant {
	seen := make(map[string]struct{})
	var out []clientModelVariant
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), client) {
			continue
		}
		for _, text := range clientModelNameVariants(name) {
			if !clientModelPlausibleName(text) {
				continue
			}
			key := clientModelASCIILower(text)
			if _, dup := seen[key]; dup {
				continue
			}
			segs := clientModelSegments(text)
			if len(segs) == 0 {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, clientModelVariant{text: text, segs: segs, prefixOnly: strings.EqualFold(text, client)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].text) > len(out[j].text) })
	return out
}

// clientModelNameVariants：名字本身、最后一个 `/` 之后、去掉开头 `<1–16 位字母数字>:`、
// 去掉结尾 ` (<数字>x)`，以及它们的任意组合（只会越变越短，闭包必然终止）。
func clientModelNameVariants(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	list := []string{name}
	seen := map[string]struct{}{name: {}}
	for i := 0; i < len(list); i++ {
		s := list[i]
		cands := []string{s}
		if k := strings.LastIndexByte(s, '/'); k >= 0 {
			cands = append(cands, s[k+1:])
		}
		if k := strings.IndexByte(s, ':'); k >= 1 && k <= 16 && clientModelAllASCIIAlnum(s[:k]) {
			cands = append(cands, s[k+1:])
		}
		if k := strings.LastIndexByte(s, '('); k >= 0 && clientModelParseMultiplier(s, k) == len(s) {
			cands = append(cands, strings.TrimRight(s[:k], " \t\r\n\v\f"))
		}
		for _, c := range cands[1:] {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if _, dup := seen[c]; dup {
				continue
			}
			seen[c] = struct{}{}
			list = append(list, c)
		}
	}
	// 带 ` (Nx)` 倍率后缀的写法不生成匹配器（其段里会含 0/11/x，白占开销还可能误吞 `名字 0.11x`），
	// 只保留去掉后缀的变体；倍率后缀靠 clientModelSwallowMultiplier 吞并。
	kept := list[:0]
	for _, s := range list {
		if k := strings.LastIndexByte(s, '('); k >= 0 && clientModelParseMultiplier(s, k) == len(s) {
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

// clientModelSegments 把名字切成字母段（含非 ASCII 字节）和数字段，统一小写；其余字节当分隔符丢弃。
// 两段之间恰好隔着一个 `/` 或 `:` 时，该字节作为后一段的首字节保留（如 `combo/auto` → `combo`、`/auto`），
// 匹配时必须原样出现，这样带前缀的整名（`vendor/Foo`、`llama3:latest`）能整体命中，
// 而不必靠「去前缀后的尾巴」（尾巴常常过不了「像模型名」门槛）。
func clientModelSegments(name string) []string {
	var segs []string
	start, kind := -1, 0 // kind: 1 数字, 2 字母
	prevEnd := 0         // 上一段的结束位置
	var lit byte         // 当前段前面隔着的 `/` 或 `:`
	flush := func(end int) {
		if start >= 0 {
			seg := clientModelASCIILower(name[start:end])
			if lit != 0 {
				seg = string(lit) + seg
			}
			segs = append(segs, seg)
			prevEnd = end
		}
		start, kind = -1, 0
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		k := 0
		switch {
		case b >= '0' && b <= '9':
			k = 1
		case clientModelIsWordByte(b):
			k = 2
		}
		if k != kind {
			flush(i)
			if k != 0 {
				start, kind = i, k
				lit = 0
				if len(segs) > 0 && prevEnd == i-1 && (name[i-1] == '/' || name[i-1] == ':') {
					lit = name[i-1]
				}
			}
		}
	}
	flush(len(name))
	return segs
}

// scrubText 把文本里的外来名（含变体）整体换成 A。无外来名或无命中 → 原样返回。
func (v clientModelView) scrubText(s string) string {
	if len(v.foreign) == 0 || s == "" {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if (i > 0 && clientModelIsASCIIAlnum(s[i-1])) || !clientModelIsWordByte(s[i]) {
			i++
			continue
		}
		start, end, ok := v.matchAt(s, i, last)
		if !ok {
			i++
			continue
		}
		_, _ = b.WriteString(s[last:start])
		_, _ = b.WriteString(v.client)
		last, i = end, end
	}
	if last == 0 {
		return s
	}
	_, _ = b.WriteString(s[last:])
	return b.String()
}

// matchAt 尝试在 s[i:] 处匹配外来名；lo 是前一次替换的结束位置，前缀吞并不得越过它。
// 返回替换区间 [start,end)（含被吞掉的前缀与倍率后缀）。
func (v clientModelView) matchAt(s string, i, lo int) (start, end int, ok bool) {
	for k := range v.foreign {
		f := &v.foreign[k]
		p := clientModelMatchSegments(s, i, f.segs)
		if p < 0 || !clientModelRightBoundary(s, p) {
			continue
		}
		start = clientModelSwallowPrefix(s, i, lo)
		if f.prefixOnly && start == i {
			continue
		}
		return start, clientModelSwallowMultiplier(s, p), true
	}
	return 0, 0, false
}

// clientModelMatchSegments 从 s[i] 起依次匹配各段（不区分大小写），段之间允许 0 或 1 个 `-._ `（以 `/` `:` 开头的段除外，见 clientModelSegments）。
// 返回匹配结束位置，失败 -1。分隔符不会与段首字节混淆，所以无需回溯。
func clientModelMatchSegments(s string, i int, segs []string) int {
	p := i
	for k, seg := range segs {
		// 以 `/` `:` 开头的段要求该字节原样出现，不走「0 或 1 个分隔符」的宽松规则。
		if k > 0 && p < len(s) && seg[0] != '/' && seg[0] != ':' && clientModelIsSepByte(s[p]) {
			p++
		}
		if len(s)-p < len(seg) {
			return -1
		}
		for j := 0; j < len(seg); j++ {
			c := s[p+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != seg[j] {
				return -1
			}
		}
		p += len(seg)
	}
	return p
}

// clientModelRightBoundary：结尾，或后一字节不在 [A-Za-z0-9_-]，且不是「`.` 后接字母数字」。
func clientModelRightBoundary(s string, p int) bool {
	if p >= len(s) {
		return true
	}
	b := s[p]
	if clientModelIsASCIIAlnum(b) || b == '_' || b == '-' {
		return false
	}
	if b == '.' && p+1 < len(s) && clientModelIsASCIIAlnum(s[p+1]) {
		return false
	}
	return true
}

// clientModelSwallowPrefix 向左吞掉紧邻的前缀，不越过 lo：
//   - `[A-Za-z0-9_.-]+/`：只吞紧邻的一段（`a/b/c/名字` 只吞 `c/`）；URL（含 `://`）和绝对路径
//     （词以 `/` 开头）里不吞，免得 `GET /v1/models/名字` 的路径被整段吃掉；
//   - `<1–4 位 ASCII 字母>:`（如 `cn:`、`us:`）：前面那一段必须整段都是 1–4 个字母，
//     所以 `model:` 这类普通词、`us-east:` 这类带连字符的都不吞；可以与 `/` 前缀连用（`us:hub/名字`）。
func clientModelSwallowPrefix(s string, i, lo int) int {
	start := i
	slashDone := false
	for start-1 >= lo && (s[start-1] == '/' || s[start-1] == ':') {
		if s[start-1] == '/' && (slashDone || clientModelInPathLikeToken(s, i)) {
			break
		}
		r := start - 1
		for r > lo && clientModelIsPrefixByte(s[r-1]) {
			r--
		}
		if r == start-1 {
			break
		}
		if s[start-1] == ':' && !clientModelIsShortLetters(s[r:start-1]) {
			break
		}
		if s[start-1] == '/' {
			slashDone = true
		}
		start = r
	}
	return start
}

// clientModelInPathLikeToken：s[i] 所在的「词」（向左到空白、引号、括号、逗号分号为止）含 `://`，或以 `/` 开头。
func clientModelInPathLikeToken(s string, i int) bool {
	ts := i
	for ts > 0 && !clientModelIsTokenBreakByte(s[ts-1]) {
		ts--
	}
	tok := s[ts:i]
	return strings.HasPrefix(tok, "/") || strings.Contains(tok, "://")
}

func clientModelIsTokenBreakByte(b byte) bool {
	if clientModelIsSpaceByte(b) {
		return true
	}
	switch b {
	case '"', '\'', '`', '<', '>', '(', ')', '[', ']', '{', '}', ',', ';':
		return true
	}
	return false
}

// clientModelIsShortLetters：1–4 个 ASCII 字母。
func clientModelIsShortLetters(s string) bool {
	if len(s) < 1 || len(s) > 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// clientModelSwallowMultiplier 吞掉名字后紧随的 `\s*\(\d+(\.\d+)?x\)`。
func clientModelSwallowMultiplier(s string, p int) int {
	j := p
	for j < len(s) && clientModelIsSpaceByte(s[j]) {
		j++
	}
	if j < len(s) && s[j] == '(' {
		if e := clientModelParseMultiplier(s, j); e > 0 {
			return e
		}
	}
	return p
}

// clientModelParseMultiplier 解析 s[i]=='(' 开头的 `(<数字>[.<数字>]x)`，返回 `)` 之后的位置，失败 -1。
func clientModelParseMultiplier(s string, i int) int {
	if i >= len(s) || s[i] != '(' {
		return -1
	}
	digits := func(j int) int {
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		return k
	}
	j := digits(i + 1)
	if j == i+1 {
		return -1
	}
	if j < len(s) && s[j] == '.' {
		k := digits(j + 1)
		if k == j+1 {
			return -1
		}
		j = k
	}
	if j+1 < len(s) && s[j] == 'x' && s[j+1] == ')' {
		return j + 2
	}
	return -1
}

func clientModelIsASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func clientModelAllASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		if !clientModelIsASCIIAlnum(s[i]) {
			return false
		}
	}
	return s != ""
}

// clientModelIsWordByte：ASCII 字母数字，或非 ASCII 字节（名字里的 Unicode 字符按字母段处理）。
func clientModelIsWordByte(b byte) bool { return clientModelIsASCIIAlnum(b) || b >= 0x80 }

func clientModelIsSepByte(b byte) bool { return b == '-' || b == '.' || b == '_' || b == ' ' }

func clientModelIsPrefixByte(b byte) bool {
	return clientModelIsASCIIAlnum(b) || b == '_' || b == '.' || b == '-'
}

func clientModelIsSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// clientModelASCIILower 只折叠 ASCII，保证字节长度不变。
func clientModelASCIILower(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// ---- 载荷改写 ----

type clientModelEdit struct {
	path  string
	value string
}

// alignPayload 对一段 JSON 载荷做字段级 + 文本级改写；没有改动时返回原切片。
func (v clientModelView) alignPayload(payload []byte) []byte {
	if v.client == "" {
		return payload
	}
	return v.alignFields(payload, 0, true)
}

func (v clientModelView) alignFields(payload []byte, nest int, allowDeltaShortcut bool) []byte {
	first := byte(0)
	for _, b := range payload {
		if !clientModelIsSpaceByte(b) {
			first = b
			break
		}
	}
	if first != '{' && first != '[' {
		return payload
	}
	if allowDeltaShortcut {
		// 流式热路径：*.delta 事件只可能带顶层 model / response.model，不做逐字段扫描。
		if t := gjson.GetBytes(payload, "type"); t.Type == gjson.String && strings.HasSuffix(t.Str, ".delta") {
			return alignClientVisibleModel(payload, v.client)
		}
	}
	var edits []clientModelEdit
	v.collectEdits(gjson.ParseBytes(payload), "", 0, nest, &edits)
	if len(edits) == 0 {
		return payload
	}
	out := payload
	for _, e := range edits {
		if next, err := sjson.SetBytes(out, e.path, e.value); err == nil {
			out = next
		}
	}
	return out
}

func (v clientModelView) collectEdits(node gjson.Result, path string, depth, nest int, out *[]clientModelEdit) {
	if depth > clientModelViewMaxDepth {
		return
	}
	switch {
	case node.IsObject():
		node.ForEach(func(key, val gjson.Result) bool {
			if key.Str == "" {
				return true
			}
			ck := clientModelCompactKey(key.Str)
			if strings.HasSuffix(ck, "fastermodel") {
				return true
			}
			sub := clientModelJoinPath(path, clientModelEscapePathKey(key.Str))
			if clientModelIsModelKey(ck) && val.Type == gjson.String {
				if val.Str != "" && val.Str != v.client {
					*out = append(*out, clientModelEdit{path: sub, value: v.client})
				}
				return true
			}
			if _, skip := clientModelViewSkipKeys[ck]; skip {
				return true
			}
			v.collectEdits(val, sub, depth+1, nest, out)
			return true
		})
	case node.IsArray():
		idx := 0
		node.ForEach(func(_, val gjson.Result) bool {
			v.collectEdits(val, clientModelJoinPath(path, strconv.Itoa(idx)), depth+1, nest, out)
			idx++
			return true
		})
	case node.Type == gjson.String:
		s := node.Str
		trimmed := strings.TrimLeft(s, " \t\r\n")
		if nest < clientModelViewMaxNest && trimmed != "" && (trimmed[0] == '{' || trimmed[0] == '[') && gjson.Valid(trimmed) {
			// 字符串里装的是 JSON（如 x-codex-turn-metadata）：按同样规则递归，改了才回写。
			nestedIn := []byte(trimmed)
			nestedOut := v.alignFields(nestedIn, nest+1, false)
			if string(nestedOut) != trimmed {
				*out = append(*out, clientModelEdit{path: path, value: s[:len(s)-len(trimmed)] + string(nestedOut)})
			}
			return
		}
		if ns := v.scrubText(s); ns != s {
			*out = append(*out, clientModelEdit{path: path, value: ns})
		}
	}
}

// clientModelCompactKey：转小写、只留 a-z0-9。
func clientModelCompactKey(k string) string {
	b := make([]byte, 0, len(k))
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b = append(b, c)
		}
	}
	return string(b)
}

// clientModelIsModelKey：compact 键等于 model，或以 model / modelid / modelname 结尾。
func clientModelIsModelKey(ck string) bool {
	return strings.HasSuffix(ck, "model") || strings.HasSuffix(ck, "modelid") || strings.HasSuffix(ck, "modelname")
}

func clientModelJoinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

// clientModelEscapePathKey 转义 gjson/sjson 路径里的特殊字符（`.` `*` `?` 等）：
// 除字母数字、`-`、`_` 外的 ASCII 字节一律加反斜杠。
func clientModelEscapePathKey(k string) string {
	need := false
	for i := 0; i < len(k); i++ {
		if c := k[i]; c < 0x80 && !clientModelIsASCIIAlnum(c) && c != '-' && c != '_' {
			need = true
			break
		}
	}
	if !need {
		return k
	}
	b := make([]byte, 0, len(k)+4)
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c < 0x80 && !clientModelIsASCIIAlnum(c) && c != '-' && c != '_' {
			b = append(b, '\\')
		}
		b = append(b, c)
	}
	return string(b)
}

// alignSSELine 对单行 SSE 做改写：只处理 `data:` 行；空 data / [DONE] / 注释 / event 行原样返回。
func (v clientModelView) alignSSELine(line string) string {
	if v.client == "" {
		return line
	}
	data, ok := extractOpenAISSEDataLine(line)
	if !ok || data == "" || data == "[DONE]" {
		return line
	}
	aligned := v.alignPayload([]byte(data))
	if len(aligned) == len(data) && string(aligned) == data {
		return line
	}
	return "data: " + string(aligned)
}

// alignSSEBody 对整段 SSE 文本逐行改写。
func (v clientModelView) alignSSEBody(body string) string {
	if v.client == "" || body == "" {
		return body
	}
	lines := strings.Split(body, "\n")
	changed := false
	for i, line := range lines {
		if aligned := v.alignSSELine(line); aligned != line {
			lines[i] = aligned
			changed = true
		}
	}
	if !changed {
		return body
	}
	return strings.Join(lines, "\n")
}

// ---- gin context 接入 ----

func setClientModelView(c *gin.Context, v clientModelView) {
	if c == nil || v.client == "" {
		return
	}
	c.Set(clientModelViewContextKey, v)
}

func clientModelViewFrom(c *gin.Context) (clientModelView, bool) {
	if c == nil {
		return clientModelView{}, false
	}
	raw, ok := c.Get(clientModelViewContextKey)
	if !ok {
		return clientModelView{}, false
	}
	v, ok := raw.(clientModelView)
	if !ok || v.client == "" {
		return clientModelView{}, false
	}
	return v, true
}

// noteClientModelViewForeign 把上游回显的名字追加进当前请求的 view；没有 view 时 no-op。
// 只收「像模型名」的值（长度 >= 4，且含数字、`-`、`/` 之一，照 recorder 的 plausible_model_name），
// 免得上游回显 `auto` 这类普通词时，把正文里的同名词也换成 A。
func noteClientModelViewForeign(c *gin.Context, names ...string) {
	v, ok := clientModelViewFrom(c)
	if !ok {
		return
	}
	kept := make([]string, 0, len(names))
	for _, n := range names {
		if clientModelPlausibleName(n) {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		return
	}
	setClientModelView(c, v.withForeign(kept...))
}

func clientModelPlausibleName(name string) bool {
	name = strings.TrimSpace(name)
	return len(name) >= 4 && strings.ContainsAny(name, "0123456789-/")
}

// scrubClientVisibleModelText：c 为 nil 或无 view → 原样。
func scrubClientVisibleModelText(c *gin.Context, s string) string {
	if v, ok := clientModelViewFrom(c); ok {
		return v.scrubText(s)
	}
	return s
}

// alignClientVisibleModelFor 等三个 …For 变体：有 view 用 view，否则退回现有 alignClientVisibleModel*。
func alignClientVisibleModelFor(c *gin.Context, payload []byte, originalModel string) []byte {
	if v, ok := clientModelViewFrom(c); ok {
		return v.alignPayload(payload)
	}
	return alignClientVisibleModel(payload, originalModel)
}

func alignClientVisibleModelInSSELineFor(c *gin.Context, line, originalModel string) string {
	if v, ok := clientModelViewFrom(c); ok {
		return v.alignSSELine(line)
	}
	return alignClientVisibleModelInSSELine(line, originalModel)
}

func alignClientVisibleModelInSSEBodyFor(c *gin.Context, body, originalModel string) string {
	if v, ok := clientModelViewFrom(c); ok {
		return v.alignSSEBody(body)
	}
	return alignClientVisibleModelInSSEBody(body, originalModel)
}
