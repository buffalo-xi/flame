package xregistry

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

// ConsulDiscovery consul 服务发现实现。
type ConsulDiscovery struct {
	client *consulapi.Client
}

func newConsulDiscovery(cfg *DiscoveryConfig) (*ConsulDiscovery, error) {
	client, err := newConsulClient(cfg.Address, cfg.Datacenter, cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("xregistry: consul discovery: %w", err)
	}
	return &ConsulDiscovery{client: client}, nil
}

// ListInstances 列出某服务的全部健康实例（passingOnly），按 ID 稳定排序。
func (d *ConsulDiscovery) ListInstances(ctx context.Context, serviceName string) ([]Instance, error) {
	q := (&consulapi.QueryOptions{}).WithContext(ctx)
	entries, _, err := d.client.Health().Service(serviceName, "", true, q)
	if err != nil {
		return nil, fmt.Errorf("xregistry: list %s: %w", serviceName, err)
	}
	out := make([]Instance, 0, len(entries))
	for _, e := range entries {
		out = append(out, Instance{
			ID:      e.Service.ID,
			Name:    e.Service.Service,
			Address: e.Service.Address,
			Port:    e.Service.Port,
			Tags:    e.Service.Tags,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ConsulRegistrar consul 服务注册实现。
type ConsulRegistrar struct {
	client *consulapi.Client
	cfg    *RegistrarConfig
}

func newConsulRegistrar(cfg *RegistrarConfig) (*ConsulRegistrar, error) {
	client, err := newConsulClient(cfg.Address, cfg.Datacenter, cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("xregistry: consul registrar: %w", err)
	}
	return &ConsulRegistrar{client: client, cfg: cfg}, nil
}

// Register 注册服务，挂 HTTP 健康检查（consul agent 主动探）。
func (r *ConsulRegistrar) Register() error {
	reg := &consulapi.AgentServiceRegistration{
		ID:      r.cfg.ServiceID,
		Name:    r.cfg.ServiceName,
		Address: r.cfg.ServiceAddr,
		Port:    r.cfg.ServicePort,
		Tags:    r.cfg.Tags,
		Check: &consulapi.AgentServiceCheck{
			HTTP:                           fmt.Sprintf("http://%s:%d%s", r.cfg.ServiceAddr, r.cfg.ServicePort, r.cfg.HealthCheckPath),
			Interval:                       r.cfg.HealthCheckInterval,
			Timeout:                        r.cfg.HealthCheckTimeout,
			DeregisterCriticalServiceAfter: r.cfg.HealthCheckDeregisterAfter,
		},
	}
	if err := r.client.Agent().ServiceRegister(reg); err != nil {
		return fmt.Errorf("xregistry: register %s: %w", r.cfg.ServiceID, err)
	}
	return nil
}

// Deregister 注销服务。
func (r *ConsulRegistrar) Deregister() error {
	if err := r.client.Agent().ServiceDeregister(r.cfg.ServiceID); err != nil {
		return fmt.Errorf("xregistry: deregister %s: %w", r.cfg.ServiceID, err)
	}
	return nil
}

// KeepRegistered 保活自愈：定期确认自己还在注册中心，被摘除（如注册中心断连）则重新注册。
// 覆盖两类自愈场景：初始注册失败、运行中失联被摘除。ctx 取消时退出循环。
func (r *ConsulRegistrar) KeepRegistered(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.ensureRegistered(); err != nil {
					log.Printf("xregistry: ensure registered %s: %v", r.cfg.ServiceID, err)
				}
			}
		}
	}()
}

// ensureRegistered 确认自己还在注册中心，被摘除则重新注册。
func (r *ConsulRegistrar) ensureRegistered() error {
	svcs, err := r.client.Agent().Services()
	if err != nil {
		return err
	}
	if _, ok := svcs[r.cfg.ServiceID]; ok {
		return nil
	}
	return r.Register()
}

// newConsulClient 构造 consul 客户端（discovery / registrar 共用）。
func newConsulClient(address, datacenter, token string) (*consulapi.Client, error) {
	cc := consulapi.DefaultConfig()
	if address != "" {
		cc.Address = address
	}
	if datacenter != "" {
		cc.Datacenter = datacenter
	}
	if token != "" {
		cc.Token = token
	}
	return consulapi.NewClient(cc)
}
