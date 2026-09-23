package antigravity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanJSONSchema_ArrayPrefixItems(t *testing.T) {
	// 模拟 Claude Code 2.1 Artifact 工具的 query.where 参数 Schema (Draft 2020-12 prefixItems 元组)
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"where": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "array",
							"prefixItems": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "string", "enum": []any{"==", "!=", ">", "<"}},
								map[string]any{},
							},
						},
						"maxItems": float64(10),
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	query, ok := cleaned["properties"].(map[string]any)["query"].(map[string]any)
	require.True(t, ok)
	where, ok := query["properties"].(map[string]any)["where"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "array", where["type"])

	whereItems, ok := where["items"].(map[string]any)
	require.True(t, ok, "where.items must be an object")
	assert.Equal(t, "array", whereItems["type"])
	// prefixItems 应在 whereItems 中被彻底移除
	assert.Nil(t, whereItems["prefixItems"])

	// 关键验证：where.items.items 必须存在且有效，不能缺失导致 Gemini 400
	innerItems, ok := whereItems["items"].(map[string]any)
	require.True(t, ok, "where.items.items must be an object")
	assert.Equal(t, "string", innerItems["type"])
}

func TestCleanJSONSchema_ArrayMissingItemsFallback(t *testing.T) {
	// 针对任何缺少 items 的 array，必须兜底注入 items: {type: string}
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{
				"type": "array",
			},
			"nested_empty_array": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "array",
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	tags, ok := props["tags"].(map[string]any)
	require.True(t, ok)
	tagsItems, ok := tags["items"].(map[string]any)
	require.True(t, ok, "tags.items must be an object")
	assert.Equal(t, "string", tagsItems["type"])

	nested, ok := props["nested_empty_array"].(map[string]any)
	require.True(t, ok)
	nestedItems, ok := nested["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", nestedItems["type"])
	nestedInnerItems, ok := nestedItems["items"].(map[string]any)
	require.True(t, ok, "nested items.items must be an object")
	assert.Equal(t, "string", nestedInnerItems["type"])
}

func TestCleanJSONSchema_ArrayExistingItemsPreserved(t *testing.T) {
	// 正常的 array items 不受影响
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"numbers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "integer",
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	numbers, ok := props["numbers"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", numbers["type"])
	items, ok := numbers["items"].(map[string]any)
	require.True(t, ok, "numbers.items must be an object")
	assert.Equal(t, "integer", items["type"])
}

func TestCleanJSONSchema_EnumOnlySchemaInTuple(t *testing.T) {
	// 测试 enum-only schema 不会被误判为 object，也不应被注入 reason 属性
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"choice": map[string]any{
				"enum": []any{"option_a", "option_b"},
			},
			"tuple_with_enum": map[string]any{
				"type": "array",
				"prefixItems": []any{
					map[string]any{
						"enum": []any{"read", "write"},
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	choice, ok := props["choice"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", choice["type"])
	assert.Nil(t, choice["properties"], "enum-only schema must NOT be treated as object with reason property")
	assert.Equal(t, []any{"option_a", "option_b"}, choice["enum"])

	tupleArray, ok := props["tuple_with_enum"].(map[string]any)
	require.True(t, ok)
	tupleItems, ok := tupleArray["items"].(map[string]any)
	require.True(t, ok, "tuple_with_enum.items must be an object")
	assert.Equal(t, "string", tupleItems["type"])
	assert.Nil(t, tupleItems["properties"])
	assert.Equal(t, []any{"read", "write"}, tupleItems["enum"])
}

func TestCleanJSONSchema_ConstKeywordConversion(t *testing.T) {
	// 测试 const 关键字自动转换为 Gemini 兼容的 enum: [const] 且具有合法 type
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"const": "ping",
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	action, ok := props["action"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, action["const"], "const keyword must be removed")
	assert.Equal(t, "string", action["type"])
	assert.Equal(t, []any{"ping"}, action["enum"])
}

func TestCleanJSONSchema_AnyOfNestedPrefixItems(t *testing.T) {
	// 测试 anyOf 分支合并进来的嵌套 prefixItems 也被完整深度清洗
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string"},
		},
		"anyOf": []any{
			map[string]any{
				"properties": map[string]any{
					"extra_tuple": map[string]any{
						"type": "array",
						"prefixItems": []any{
							map[string]any{"type": "integer"},
						},
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	require.NotNil(t, props["extra_tuple"])
	extraTuple, ok := props["extra_tuple"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, extraTuple["prefixItems"], "nested prefixItems from anyOf merge must be cleaned")
	extraItems, ok := extraTuple["items"].(map[string]any)
	require.True(t, ok, "extra_tuple.items must be an object")
	assert.Equal(t, "integer", extraItems["type"])
}

func TestCleanJSONSchema_EmptyPrefixItems(t *testing.T) {
	// 验证空 prefixItems: [] 也被安全删除，不遗留非法关键字
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"empty_tuple": map[string]any{
				"type":        "array",
				"prefixItems": []any{},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	emptyTuple, ok := props["empty_tuple"].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, emptyTuple["prefixItems"], "empty prefixItems array must be removed")
	emptyTupleItems, ok := emptyTuple["items"].(map[string]any)
	require.True(t, ok, "empty_tuple.items must be an object")
	assert.Equal(t, "string", emptyTupleItems["type"])
}

func cleanedProp(t *testing.T, cleaned map[string]any, name string) map[string]any {
	t.Helper()
	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok, "properties must be an object")
	prop, ok := props[name].(map[string]any)
	require.True(t, ok, "property %q must be an object", name)
	return prop
}

func TestCleanJSONSchema_ConstNonStringValues(t *testing.T) {
	// const is normalized to enum:[const]; non-string enum values are then
	// stringified (Gemini only accepts string enums), so the final type is string
	// and the node must never be mistaken for an object with an injected reason.
	cases := []struct {
		name     string
		constVal any
		wantEnum []any
	}{
		{name: "int", constVal: 7, wantEnum: []any{"7"}},
		{name: "int64", constVal: int64(7), wantEnum: []any{"7"}},
		{name: "float64", constVal: float64(1.5), wantEnum: []any{"1.5"}},
		{name: "bool", constVal: true, wantEnum: []any{"true"}},
		{name: "fallback", constVal: []any{"x"}, wantEnum: []any{"[x]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := CleanJSONSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"v": map[string]any{"const": tc.constVal},
				},
			})
			v := cleanedProp(t, cleaned, "v")
			assert.Equal(t, "string", v["type"])
			assert.Equal(t, tc.wantEnum, v["enum"])
			assert.NotContains(t, v, "const")
			assert.NotContains(t, v, "properties", "const scalar must not be turned into an object")
			assert.NotContains(t, v, "required")
		})
	}
}

func TestCleanJSONSchema_ConstKeepsExistingEnum(t *testing.T) {
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"v": map[string]any{
				"type":  "string",
				"const": "a",
				"enum":  []any{"a", "b"},
			},
		},
	})
	v := cleanedProp(t, cleaned, "v")
	assert.Equal(t, "string", v["type"])
	assert.Equal(t, []any{"a", "b"}, v["enum"], "existing enum must not be overwritten by const")
	assert.NotContains(t, v, "const")
}

func TestCleanJSONSchema_EnumOnlyNonStringValues(t *testing.T) {
	cases := []struct {
		name     string
		enum     []any
		wantEnum []any
	}{
		{name: "int", enum: []any{1, 2}, wantEnum: []any{"1", "2"}},
		{name: "float64", enum: []any{float64(1), float64(2.5)}, wantEnum: []any{"1", "2.5"}},
		{name: "bool", enum: []any{true, false}, wantEnum: []any{"true", "false"}},
		{name: "string", enum: []any{"a"}, wantEnum: []any{"a"}},
		{name: "empty", enum: []any{}, wantEnum: []any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := CleanJSONSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"v": map[string]any{"enum": tc.enum},
				},
			})
			v := cleanedProp(t, cleaned, "v")
			assert.Equal(t, "string", v["type"])
			assert.Equal(t, tc.wantEnum, v["enum"])
			assert.NotContains(t, v, "properties", "enum-only schema must not get a reason property")
			assert.NotContains(t, v, "required")
		})
	}
}

func TestCleanJSONSchema_ItemsWithoutTypeInfersArray(t *testing.T) {
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"list": map[string]any{
				"items": map[string]any{"type": "integer"},
			},
		},
	})
	list := cleanedProp(t, cleaned, "list")
	assert.Equal(t, "array", list["type"])
	items, ok := list["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "integer", items["type"])
}

func TestCleanJSONSchema_ArrayInvalidItemsFallback(t *testing.T) {
	cases := []struct {
		name  string
		items any
	}{
		{name: "nil", items: nil},
		{name: "empty object", items: map[string]any{}},
		{name: "bool true", items: true},
		{name: "bool false", items: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := CleanJSONSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"arr": map[string]any{"type": "array", "items": tc.items},
				},
			})
			arr := cleanedProp(t, cleaned, "arr")
			items, ok := arr["items"].(map[string]any)
			require.True(t, ok, "arr.items must be replaced with a schema object")
			assert.Equal(t, "string", items["type"])
		})
	}
}

func TestCleanJSONSchema_PrefixItemsClosedTuple(t *testing.T) {
	// Draft 2020-12 closed tuple: items:false must be replaced by the best prefixItems entry.
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pair": map[string]any{
				"type": "array",
				"prefixItems": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "object", "properties": map[string]any{"k": map[string]any{"type": "string"}}},
				},
				"items": false,
			},
		},
	})
	pair := cleanedProp(t, cleaned, "pair")
	assert.NotContains(t, pair, "prefixItems")
	items, ok := pair["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", items["type"], "highest-scoring tuple member should win")
	itemProps, ok := items["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, itemProps, "k")
}

func TestCleanJSONSchema_PrefixItemsKeepsExistingItems(t *testing.T) {
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tuple": map[string]any{
				"type":        "array",
				"prefixItems": []any{map[string]any{"type": "string"}},
				"items":       map[string]any{"type": "integer"},
			},
		},
	})
	tuple := cleanedProp(t, cleaned, "tuple")
	assert.NotContains(t, tuple, "prefixItems")
	items, ok := tuple["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "integer", items["type"])
}

func TestCleanJSONSchema_PrefixItemsWithoutSchemaMembers(t *testing.T) {
	// No usable tuple member: fall back to a string items schema.
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tuple": map[string]any{
				"type":        "array",
				"prefixItems": []any{nil},
			},
		},
	})
	tuple := cleanedProp(t, cleaned, "tuple")
	assert.NotContains(t, tuple, "prefixItems")
	items, ok := tuple["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", items["type"])
}

func TestCleanJSONSchema_PropertiesAndItemsBothCleaned(t *testing.T) {
	// A schema declaring both properties and items must have both subtrees cleaned.
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"odd": map[string]any{
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "format": "uuid"},
				},
				"items": map[string]any{
					"type":        "array",
					"prefixItems": []any{map[string]any{"type": "boolean"}},
				},
			},
		},
	})
	odd := cleanedProp(t, cleaned, "odd")
	items, ok := odd["items"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, items, "prefixItems")
	inner, ok := items["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "boolean", inner["type"])
}

func TestCleanJSONSchema_AnyOfMergedItemsCleaned(t *testing.T) {
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"v": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "null"},
					map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "array", "prefixItems": []any{map[string]any{"type": "number"}}},
					},
				},
			},
		},
	})
	v := cleanedProp(t, cleaned, "v")
	assert.NotContains(t, v, "anyOf")
	assert.Equal(t, "array", v["type"])
	items, ok := v["items"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, items, "prefixItems")
	inner, ok := items["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "number", inner["type"])
}

func TestScoreSchemaOption_EnumAndConst(t *testing.T) {
	assert.Equal(t, 1, scoreSchemaOption(map[string]any{"enum": []any{"a"}}))
	assert.Equal(t, 1, scoreSchemaOption(map[string]any{"const": "a"}))
	assert.Equal(t, 0, scoreSchemaOption(map[string]any{"type": "null"}))
	assert.Equal(t, 0, scoreSchemaOption("not a schema"))
}

func TestCleanJSONSchema_LegacyTupleItemsArray(t *testing.T) {
	// Draft 4-2019 tuple form items:[A,B] is collapsed to the best member; with
	// properties present the items subtree must still be processed.
	cleaned := CleanJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tuple": map[string]any{
				"type": "array",
				"items": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "array"},
				},
			},
			"empty_tuple": map[string]any{
				"type":  "array",
				"items": []any{},
			},
		},
		"items": []any{map[string]any{"type": "integer"}},
	})
	tuple := cleanedProp(t, cleaned, "tuple")
	items, ok := tuple["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", items["type"])
	inner, ok := items["items"].(map[string]any)
	require.True(t, ok, "collapsed array member must get an items fallback")
	assert.Equal(t, "string", inner["type"])

	emptyTuple := cleanedProp(t, cleaned, "empty_tuple")
	emptyItems, ok := emptyTuple["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", emptyItems["type"])

	rootItems, ok := cleaned["items"].(map[string]any)
	require.True(t, ok, "root items must be cleaned even when properties are present")
	assert.Equal(t, "integer", rootItems["type"])
}
