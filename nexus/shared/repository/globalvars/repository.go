package globalvars

import (
	"flame/tools/xsql"
)

// 变量类型。与前端下拉、模板注入的类型转换共用一份取值，
// 别在 handler / resolve 各写一套字符串字面量。
const (
	KindText   = "text"
	KindSecret = "secret"
	KindNumber = "number"
	KindBool   = "bool"
)

// GlobalVar 一行全局变量。
//
// Value 在进程内**始终是明文**，加密只发生在落库那一刻：kind=secret 的值由
// 仓储层加密成 `enc:v1:...`（见 shared/secureval），读出来再解密。也就是说
// 「静态加密」是仓储的职责，调用方（handler / resolve）拿到的都是明文，
// 不需要也不要自己处理密文。
//
// 接口出参的掩码是另一件事，在 admin/globalvars 做；导出资产包永远不带值 ——
// 包里只有引用名。
type GlobalVar struct {
	Name        string    `json:"name"`
	Value       string    `json:"value"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	UpdatedBy   xsql.UUID `json:"updated_by"`
	UpdatedAt   xsql.Time `json:"updated_at"`
}
