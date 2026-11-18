package models

import "fmt"

// CheckQuota 检查资源用量是否已达上限（max < 0 表示不限制，即 -1）。
// kind 用于错误提示（agent / skill / flow）。
//
// ⚠️ 新增"创建"入口时必须调用本函数，否则该入口不受配额约束。
func CheckQuota(kind string, current, max int) error {
	if max < 0 {
		return nil // -1 = 不限制
	}
	if current >= max {
		return fmt.Errorf("%s 数量已达上限（%d/%d），请先删除不再使用的 %s", kind, current, max, kind)
	}
	return nil
}
