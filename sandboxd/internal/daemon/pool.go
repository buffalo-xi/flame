package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

var (
	errPoolClosed = errors.New("script pool closed")
	errPoolBusy   = errors.New("sandbox busy")
)

// slotPool 单语言的 worker 槽位池 + 并发闸。
//
// 设计要点：
//   - sem 是并发闸（容量 = max_concurrent）。超出时等待 acquire 超时后报「繁忙」，
//     绝不无限阻塞 —— binding transform 等场景是嵌套调用，无限阻塞会自我死锁。
//   - 池化对象是常驻的 codehost 宿主（跑在隔离单元内）：复用隔离单元、每次
//     execute 由 codehost fork 全新解释器 —— 复用隔离单元、解释器进程级干净，
//     天然零残留。
//   - 归还策略只有一档：「一次 execute 已完成」的干净 worker 归还复用，半截状态
//     （中断 / 超时 / 协议错误）销毁。解释器进程本来就每次新建，没有跨请求残留。
//   - refiller 维持 idle.initial 个空闲对象：没有它，池子会被抽干，预热就白做了。
//     idle.initial 为 0 时它是空转的。
type slotPool struct {
	lang             string
	cfg              LangConfig
	isolate          sandbox.Sandbox
	codehostBinary string // 常驻 codehost 宿主的二进制入口
	tmpRoot          string         // 可写目录的宿主根：每个 worker 在其下建独占子目录
	limits           sandbox.Limits // 启动 worker 时使用的资源上限（来自 script.limits 并 clamp）

	idle      chan *Worker
	sem       chan struct{}
	kick      chan struct{} // refiller 的即时补量信号（容量 1，多余信号合并）
	closed    chan struct{}
	closeOnce sync.Once
}

// refillInterval refiller 的兜底巡检间隔：即时补量走 kick，这个只负责自愈
// （worker 自己死掉、或补量失败后的重试）。
const refillInterval = time.Second

// newSlotPool 创建单语言槽位池。
//
// 池随语言条目存在而存在（code.pools.<lang> 在 = 这个池就在）；
// 「不预热 / 不留驻」由 idle 的数值表达（都取 0 时池自然空转）。
func newSlotPool(lang string, cfg LangConfig, iso sandbox.Sandbox, codehostBinary, tmpRoot string, limits sandbox.Limits) *slotPool {
	p := &slotPool{
		lang:             lang,
		cfg:              cfg,
		isolate:          iso,
		codehostBinary: codehostBinary,
		tmpRoot:          tmpRoot,
		limits:           limits,
		sem:              make(chan struct{}, cfg.MaxConcurrent),
		kick:             make(chan struct{}, 1),
		closed:           make(chan struct{}),
		idle:             make(chan *Worker, cfg.Idle.Max),
	}

	// 池随语言条目存在而存在：refiller / janitor 无条件启动。
	// idle.initial 为 0 时 refill 开头就 return（空转），janitor 也只面对一个空池。
	p.startRefiller()
	p.startJanitor()
	return p
}

// startRefiller 维持 idle.initial 个空闲对象：启动预热 + 运行期补量。
//
// 每次归还一个「半截状态」的 worker 都会销毁它，没有补量的话池子会被抽干，
// 之后每个请求都退化成「现场 spawn」（也就是预热失去意义）。
func (p *slotPool) startRefiller() {
	go func() {
		p.refill()
		xlog.Info("sandboxd: %s pool ready (%s=%d/%d)",
			p.lang, p.idleWhat(), p.idleCount(), p.cfg.Idle.Initial)
		t := time.NewTicker(refillInterval)
		defer t.Stop()
		for {
			select {
			case <-p.closed:
				return
			case <-p.kick:
			case <-t.C:
			}
			p.refill()
		}
	}()
}

// idleWhat 空闲计量的名字（只用于日志）。
func (p *slotPool) idleWhat() string { return "idle" }

// idleCount 空闲对象数。
func (p *slotPool) idleCount() int { return len(p.idle) }

// refill 把空闲对象补到 idle.initial（同步创建：本函数只在 refiller
// goroutine 里跑，冷启动开销不落在任何请求路径上）。
func (p *slotPool) refill() {
	target := p.cfg.Idle.Initial
	if target <= 0 {
		return
	}

	if p.idle == nil {
		return
	}
	for len(p.idle) < target {
		select {
		case <-p.closed:
			return
		default:
		}
		w, err := spawnCodehost(p.isolate, p.codehostBinary, p.tmpRoot, p.limits)
		if err != nil {
			xlog.Error("sandboxd: refill %s codehost failed: %v", p.lang, err)
			return // 起不来就别死循环，等下一次 kick / 巡检
		}
		select {
		case p.idle <- w:
		case <-p.closed:
			w.Destroy()
			return
		default:
			w.Destroy() // 池满（并发归还竞争），丢弃
			return
		}
	}
}

// kickRefill 通知 refiller 尽快补量（非阻塞：信号有意义时就够了）。
func (p *slotPool) kickRefill() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// startJanitor 定期清理死掉的空闲 worker。
//
// 它只负责「清」，补量交给 refiller（两者都收敛到同一个 idle 池）。
func (p *slotPool) startJanitor() {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.dropDeadIdle()
			case <-p.closed:
				return
			}
		}
	}()
}

// dropDeadIdle 逐个探活空闲 worker，死的丢弃，活的放回。
//
// 这是唯一还会主动 ping 的地方（30s 一次，不在请求路径上）：它要发现的是
// 「进程还在、但已经不再应答」这种 watch 看不出来的假死（例如 codehost 卡死）。
func (p *slotPool) dropDeadIdle() {
	var keep []*Worker
loop:
	for {
		select {
		case w := <-p.idle:
			if w.ping() == nil {
				keep = append(keep, w)
			} else {
				w.Destroy()
			}
		default:
			break loop
		}
	}
	for _, w := range keep {
		select {
		case p.idle <- w:
		default:
			w.Destroy()
		}
	}
	p.kickRefill() // 丢掉几个就补几个（restart 档下空闲池里也可能自然死掉）
}

// Acquire 借出一个 worker；reused 表示是否复用了空闲池中的实例。
func (p *slotPool) Acquire(ctx context.Context) (*Worker, bool, error) {
	// 1) 并发闸（带超时，不无限阻塞）
	wait := time.Duration(p.cfg.AcquireTimeoutSec) * time.Second
	if wait <= 0 {
		// 兜底：配置层（applyDefaults）总会补上缺省（15s），这里只为「配置缺省被删掉」
		// 这种不可能在生产出现的路径保底 —— 取值与 config.go 的缺省保持一致，别再留第二个数。
		wait = 15 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-p.closed:
		return nil, false, errPoolClosed
	case <-timer.C:
		return nil, false, fmt.Errorf("%w: %s 并发已达上限 %d", errPoolBusy, p.lang, p.cfg.MaxConcurrent)
	}

	// 2) 优先复用空闲 worker。
	//    这里不发 ping：一次探活是「写帧 + 等 pong」的同步往返，而 lua 是 µs 级的
	//    高频调用（循环条件 / 分支条件 / binding transform），这点固定开销会盖过
	//    脚本本身。存活性由 Worker.watch 维护（进程退出立即置 alive=false），
	//    janitor 每 30s 再主动 ping 一次兜底。
	if p.idle != nil {
	loop:
		for {
			select {
			case cand := <-p.idle:
				if cand.Alive() {
					return cand, true, nil
				}
				cand.Destroy()
				p.kickRefill()
			default:
				break loop
			}
		}
	}

	// 3) 现场启动
	w, err := spawnCodehost(p.isolate, p.codehostBinary, p.tmpRoot, p.limits)
	if err != nil {
		<-p.sem
		return nil, false, err
	}
	return w, false, nil
}

// Release 归还 worker。
//
// 只有一档：reusable 为 false（执行中断、超时、协议错误、连接异常）时销毁 ——
// 状态不明的 worker 绝不复用；干净且进程仍在（watch 标记的 alive）才放回空闲池。
// 归还同样不发 ping：进程若已退出，watch 早已置 alive=false，这里直接销毁。
// 解释器进程本来就每次新建，没有跨请求残留，所以不再有 lifecycle（reuse / restart）分档。
func (p *slotPool) Release(w *Worker, reusable bool) {
	<-p.sem // 先释放并发额度

	if w == nil {
		return
	}

	select {
	case <-p.closed:
		p.destroyWorker(w)
		return
	default:
	}

	if !reusable || p.idle == nil || !w.Alive() {
		w.Destroy()
		p.kickRefill()
		return
	}

	select {
	case p.idle <- w:
	case <-p.closed:
		w.Destroy()
	default:
		w.Destroy() // 池已满
	}
}

// destroyWorker 彻底销毁一个 worker。
func (p *slotPool) destroyWorker(w *Worker) {
	w.Destroy()
}

// Close 关闭池子并销毁全部空闲 worker（借出中的会在 Release 时销毁）。
func (p *slotPool) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)

		if p.idle == nil {
			return
		}
		for {
			select {
			case w := <-p.idle:
				w.Destroy()
			default:
				return
			}
		}
	})
}
