package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TrimRequestBodyModel 去掉请求体顶层 model 字段首尾的空白（含 NBSP、全角空格等 Unicode 空白），
// 其余字节原样保留。
//
// 网关入口在读取 model 之前调用一次，让分组/渠道映射、账号调度、转发给上游的请求体、计价和
// 用量日志看到的是同一个名字。只做 TrimSpace，不改大小写：计价与渠道查找各自已有大小写归一化，
// 这里不替它们做决定。
//
// model 缺失、不是字符串或本来就干净时原样返回同一个切片；改写失败时同样原样返回，由后续
// 校验按原有路径处理。
func TrimRequestBodyModel(body []byte) []byte {
	model := gjson.GetBytes(body, "model")
	if model.Type != gjson.String {
		return body
	}
	raw := model.String()
	trimmed := strings.TrimSpace(raw)
	if trimmed == raw {
		return body
	}
	out, err := sjson.SetBytes(body, "model", trimmed)
	if err != nil {
		return body
	}
	return out
}
