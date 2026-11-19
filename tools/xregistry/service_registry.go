package xregistry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Type is the registry kind.
type Type string

const (
	TypeConsul Type = "consul"
	TypeNacos  Type = "nacos"
)

// Instance is one healthy service instance (returned by the discovery side).
type Instance struct {
	ID      string
	Name    string
	Address string
	Port    int
	Tags    []string
}

// Discovery is the service discovery interface (consumer side): used by sandboxg and nexus_gw.
type Discovery interface {
	// ListInstances lists every healthy instance of a service, stably sorted by ID.
	ListInstances(ctx context.Context, serviceName string) ([]Instance, error)
}

// Registrar is the service registration interface (provider side): used by sandboxd and nexus_gw.
type Registrar interface {
	// Register registers the service, including its health check.
	Register() error
	// Deregister unregisters the service.
	Deregister() error
	// KeepRegistered is the liveness loop: it periodically confirms the service is still registered
	// and re-registers when it has been evicted (e.g. the registry dropped the connection).
	// It returns when ctx is cancelled; interval <= 0 means the default.
	KeepRegistered(ctx context.Context, interval time.Duration)
}

// DiscoveryConfig configures service discovery.
type DiscoveryConfig struct {
	Type       Type
	Address    string
	Datacenter string
	Token      string
	// nacos placeholders: Namespace / Group / Cluster and friends, to be added later.
}

// RegistrarConfig configures service registration.
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

// NewDiscovery creates a discovery client.
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

// NewRegistrar creates a registration client.
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

// applyRegistrarDefaults fills in the registration defaults.
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

// --- nacos placeholder: the interface is in place, the implementation follows via nacos-sdk-go ---

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
