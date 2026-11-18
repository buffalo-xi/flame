package globalvars

// NewRepository 创建全局变量存储实例
func NewRepository() Repository {
	return &PostgresRepository{}
}

// Repository 全局变量存储接口。
//
// 没有分页：变量的量级是「十几到几十条」，用不到分页，
// 加了反而让调用方多一层翻页逻辑。真要几百条再说。
type Repository interface {
	// List 列出一个租户的全部变量（按 name 升序）。
	List(schema string) ([]GlobalVar, error)
	// Get 取一条；不存在返回 (nil, nil)，让调用方自己决定是报错还是新建。
	Get(schema, name string) (*GlobalVar, error)
	// Upsert 按 name 写入或覆盖（created_by 只在首次创建时落，覆盖不动它）。
	Upsert(schema string, v *GlobalVar) error
	// Delete 删除一条，返回是否真的删掉了（不存在返回 false，不报错）。
	Delete(schema, name string) (bool, error)
}
