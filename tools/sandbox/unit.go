package sandbox

import "context"

// Reusable 由「能复用隔离单元」的实现提供（目前只有 docker）。
//
// 动机：一次隔离边界的建立成本远高于一次解释器启动
// （docker 容器 300ms~1s；python/node 解释器 20~60ms）。而 python/js 这类
// 「单次几十 ms ~ 秒级」的调用，需要的其实只是「跨请求零残留」，
// 并不需要每次重建整个隔离边界。
//
// 于是把「隔离单元」与「执行进程」拆开：单元复用，进程每请求新建。
// 不实现本接口的实现（none / nsjail）由调用方自动退化成 L3
// （单元与进程同生共死），见 nexus/docs/sandbox-design.md §9。
type Reusable interface {
	// Acquire 创建一个空闲单元：隔离边界已就绪，内部还没跑任何进程。
	// spec 描述单元的构造参数（docker 下即容器参数：镜像、挂载、上限、网络）。
	Acquire(ctx context.Context, spec *Spec) (Unit, error)
}

// Unit 一个可复用单元：隔离边界常驻，Exec 在其内部启动新进程。
//
// 生命周期：Acquire 建出来 → 多次 Exec（每次一个短命进程）→ Close 销毁。
// Exec 返回的 Process 与 Sandbox.Start 的语义完全一致（含帧协议要用的
// stdin/stdout、以及退出码归一化为 *ExitError）。
//
// 与复用相关的两个约定（见设计文档 §9.3）：
//   - 杀「单元内进程」不等于销毁单元：关 stdin、必要时在单元内补刀；
//     Close 才是销毁单元本身。
//   - 单元一旦不可信（补刀后仍不干净、Exec 失败）应直接 Close 丢弃，
//     而不是继续复用 —— 「不干净」是最坏的复用前提。
type Unit interface {
	// Exec 在单元内启动一个进程。
	// spec.Dir 是「单元内路径」（须已存在）；可写目录由调用方在挂载点下自行准备。
	Exec(ctx context.Context, spec *Spec) (Process, error)
	// Close 销毁单元（含清理其中残留的进程）。
	Close() error
	// Name 单元标识（如容器 ID），用于日志与排障。
	Name() string
}

// OrphanCleaner 由「会在宿主留下可识别残留」的实现提供（docker 的容器）。
//
// 进程被强杀、宿主断电等情况下，单元来不及 Close 就留在系统里。
// 启动时按实现自己的标记（如容器 label）回收，避免容器无限堆积。
type OrphanCleaner interface {
	// CleanOrphans 回收本服务遗留的孤立单元，返回清理数量。
	CleanOrphans(ctx context.Context) (int, error)
}
