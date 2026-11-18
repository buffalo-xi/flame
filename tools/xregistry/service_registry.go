package xregistry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Type 注册中心类型。
type Type string

const (
	TypeConsul Type = "consul"
	TypeNacos  Type = "nacos"
)

// Instance 一个健康服务实例（发现侧返回）。
type Instance struct {
	ID      string
	Name    string
	Address string
	Port    int
	Tags    []string
}

// Discovery 服务发现接口（消费者视角）：sandboxg、nexus_gw 用。
type Discovery interface {
	// ListInstances 列出某服务的全部健康实例，按 ID 稳定排序。
	ListInstances(ctx context.Context, serviceName string) ([]Instance, error)
}

// Registrar 服务注册接口（提供者视角）：sandboxd、nexus_gw 用。
type Registrar interface {
	// Register 注册服务（含健康检查）。
	Register() error
	// Deregister 注销服务。
	Deregister() error
	// KeepRegistered 保活自愈：定期确认自己还在注册中心，被摘除（如注册中心断连）
	// 则重新注册。ctx 取消时退出循环；interval <= 0 时用默认值。
	KeepRegistered(ctx context.Context, interval time.Duration)
}

// DiscoveryConfig 服务发现配置。
type DiscoveryConfig struct {
	Type       Type
	Address    string
	Datacenter string
	Token      string
	// nacos 预留：Namespace / Group / Cluster 等，将来补
}

// RegistrarConfig 服务注册配置。
type RegistrarConfig struct {
	Type       Type
	Address    string
	Datacenter string
	Token      string

	ServiceName string
	ServiceID   string
	ServiceAddr string
	ServicePort int
	Tags        []string

	HealthCheckPath            string
	HealthCheckInterval        string
	HealthCheckTimeout         string
	HealthCheckDeregisterAfter string
}

// NewDiscovery 创建发现客户端。
func NewDiscovery(cfg *DiscoveryConfig) (Discovery, error) {
	switch cfg.Type {
	case TypeConsul:
		return newConsulDiscovery(cfg)
	case TypeNacos:
		return newNacosDiscovery(cfg)
	default:
		return nil, fmt.Errorf("xregistry: unsupported discovery type: %s", cfg.Type)
	}
}

// NewRegistrar 创建注册客户端。
func NewRegistrar(cfg *RegistrarConfig) (Registrar, error) {
	applyRegistrarDefaults(cfg)
	switch cfg.Type {
	case TypeConsul:
		return newConsulRegistrar(cfg)
	case TypeNacos:
		return newNacosRegistrar(cfg)
	default:
		return nil, fmt.Errorf("xregistry: unsupported registrar type: %s", cfg.Type)
	}
}

// applyRegistrarDefaults 注册配置默认值。
func applyRegistrarDefaults(cfg *RegistrarConfig) {
	if cfg.ServiceID == "" {
		cfg.ServiceID = fmt.Sprintf("%s-%d", cfg.ServiceName, cfg.ServicePort)
	}
	if cfg.HealthCheckPath == "" {
		cfg.HealthCheckPath = "/health"
	}
	if cfg.HealthCheckInterval == "" {
		cfg.HealthCheckInterval = "10s"
	}
	if cfg.HealthCheckTimeout == "" {
		cfg.HealthCheckTimeout = "10s"
	}
	if cfg.HealthCheckDeregisterAfter == "" {
		cfg.HealthCheckDeregisterAfter = "30s"
	}
}

// —— nacos 占位：接口先就位，将来用 nacos-sdk-go 补实现 ——

var errNacosNotImplemented = errors.New("xregistry: nacos not implemented yet")

type NacosDiscovery struct{}

func newNacosDiscovery(*DiscoveryConfig) (*NacosDiscovery, error) {
	return nil, errNacosNotImplemented
}

func (*NacosDiscovery) ListInstances(context.Context, string) ([]Instance, error) {
	return nil, errNacosNotImplemented
}

type NacosRegistrar struct{}

func newNacosRegistrar(*RegistrarConfig) (*NacosRegistrar, error) {
	return nil, errNacosNotImplemented
}

func (*NacosRegistrar) Register() error                               { return errNacosNotImplemented }
func (*NacosRegistrar) Deregister() error                             { return errNacosNotImplemented }
func (*NacosRegistrar) KeepRegistered(context.Context, time.Duration) {}
