package globalvars

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	repo "flame/nexus/shared/repository/globalvars"
	"flame/tools/xlog"
)

// 取值层：把 <schema>.global_vars 里的行转成「可直接注入模板」的 map。
//
// 与 shared/repository/globalvars 的分工：那份管 SQL，这份管缓存 + 类型转换。
// 调用方（节点执行、环境变量解析）只应该看到 Resolve，别自己查库。

// cacheTTL 进程内缓存时长。
//
// 为什么不做 Redis 缓存：值与密钥同表，写进 Redis 等于把明文再复制一份到另一个
// 访问面；而变量的读取频率是「每次运行一次快照」，5 秒 TTL 足够削掉绝大部分
// 重复查询。多实例下改值后最多 5 秒生效，与 Redis 方案同量级。
const cacheTTL = 5 * time.Second

type entry struct {
	values map[string]any
	// secrets 与 values 一起缓存：判断「某个引用是不是密钥类型」的调用点在
	// 节点执行的热路径上（每次渲染提示词前），单独查库不值得。
	secrets map[string]bool
	at      time.Time
}

var cache = struct {
	sync.RWMutex
	m map[string]entry
}{m: make(map[string]entry)}

// Resolve 返回某租户的全局变量（按 kind 转成原生类型：number→float64、bool→bool）。
//
// 失败一律返回 error，**不降级成空 map** —— 空 map 会让
// 「没配置也能跑」变成线上现实：节点输入取到 nil 之后，脚本/请求会带着空值继续跑，
// 那种错误排查起来比直接失败贵得多。
func Resolve(schema string) (map[string]any, error) {
	e, err := snapshot(schema)
	if err != nil {
		return nil, err
	}
	return copyValues(e.values), nil
}

// SecretNames 返回该租户里 kind=secret 的变量名集合。
//
// 用途是「拦住不该出现的地方」：提示词、会话记录、SSE 里一旦出现密钥真值就是泄漏，
// 而这些通道拿到的是渲染后的字符串，事后分不出哪一段来自密钥 —— 只能在渲染前判。
func SecretNames(schema string) (map[string]bool, error) {
	e, err := snapshot(schema)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(e.secrets))
	for name := range e.secrets {
		out[name] = true
	}
	return out, nil
}

// snapshot 取缓存，过期或未命中则回源重建。
func snapshot(schema string) (entry, error) {
	if schema == "" {
		return entry{}, fmt.Errorf("全局变量：租户 schema 为空（租户未初始化）")
	}

	cache.RLock()
	e, ok := cache.m[schema]
	cache.RUnlock()
	if ok && time.Since(e.at) < cacheTTL {
		return e, nil
	}

	rows, err := repo.NewRepository().List(schema)
	if err != nil {
		// 读失败不写缓存：下一次请求立刻重试，而不是把「坏了」缓存 5 秒
		xlog.Error("读取全局变量失败 schema=%s: %v", schema, err)
		return entry{}, fmt.Errorf("读取全局变量失败: %w", err)
	}

	values := make(map[string]any, len(rows))
	secrets := make(map[string]bool)
	for _, row := range rows {
		v, cerr := convert(row)
		if cerr != nil {
			return entry{}, cerr
		}
		values[row.Name] = v
		if row.Kind == repo.KindSecret {
			secrets[row.Name] = true
		}
	}

	e = entry{values: values, secrets: secrets, at: time.Now()}
	cache.Lock()
	cache.m[schema] = e
	cache.Unlock()
	return e, nil
}

// Invalidate 让本实例的缓存立刻失效（写完变量后调用；多实例靠 TTL 自然过期）。
func Invalidate(schema string) {
	cache.Lock()
	delete(cache.m, schema)
	cache.Unlock()
}

// copyValues 浅拷贝：值都是标量，浅拷贝就够；
// 返回内部 map 的引用会让调用方（哪怕是误改）把缓存带脏。
func copyValues(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// convert 按 kind 转成原生类型，让模板里能直接比较 / 参与运算，
// 而不是所有变量都变成字符串。
func convert(row repo.GlobalVar) (any, error) {
	switch row.Kind {
	case repo.KindNumber:
		if strings.TrimSpace(row.Value) == "" {
			return float64(0), nil
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(row.Value), 10, 64); err == nil {
			return float64(n), nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(row.Value), 64)
		if err != nil {
			return nil, fmt.Errorf("全局变量 %s 声明为 number，但值 %q 不是数字", row.Name, row.Value)
		}
		return f, nil
	case repo.KindBool:
		// 宽松解析：配置里写 true/false 之外，也接受 1/0、yes/no、on/off
		switch strings.ToLower(strings.TrimSpace(row.Value)) {
		case "", "false", "0", "no", "off":
			return false, nil
		case "true", "1", "yes", "on":
			return true, nil
		default:
			return nil, fmt.Errorf("全局变量 %s 声明为 bool，但值 %q 不是布尔", row.Name, row.Value)
		}
	case repo.KindSecret, repo.KindText:
		return row.Value, nil
	default:
		return nil, fmt.Errorf("全局变量 %s 的类型 %q 不受支持（只支持 text / secret / number / bool）", row.Name, row.Kind)
	}
}
