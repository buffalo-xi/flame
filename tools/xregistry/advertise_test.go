package xregistry

import "testing"

func TestResolveAdvertise(t *testing.T) {
	cases := []struct {
		name        string
		serviceAddr string
		listen      string
		iface       string
		wantHost    string
		wantPort    int
		wantErr     bool
	}{
		{
			name:     "concrete bind address is used as-is",
			listen:   "10.0.0.12:27081",
			wantHost: "10.0.0.12",
			wantPort: 27081,
		},
		{
			name:        "explicit host wins over a wildcard bind",
			serviceAddr: "192.168.1.20",
			listen:      "0.0.0.0:27081",
			wantHost:    "192.168.1.20",
			wantPort:    27081,
		},
		{
			name:        "explicit host:port overrides the listen port",
			serviceAddr: "192.168.1.20:30000",
			listen:      "0.0.0.0:27081",
			wantHost:    "192.168.1.20",
			wantPort:    30000,
		},
		{
			name:        "loopback is kept when written explicitly",
			serviceAddr: "127.0.0.1",
			listen:      "0.0.0.0:27081",
			wantHost:    "127.0.0.1",
			wantPort:    27081,
		},
		{
			name:        "an unknown interface fails instead of guessing",
			listen:      "0.0.0.0:27081",
			iface:       "no-such-interface",
			wantErr:     true,
		},
		{
			name:    "listen without a port is an error",
			listen:  "0.0.0.0",
			wantErr: true,
		},
		{
			name:        "a bare port is rejected",
			serviceAddr: "192.168.1.20:",
			listen:      "0.0.0.0:27081",
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, err := ResolveAdvertise(tc.serviceAddr, tc.listen, tc.iface)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %s:%d", host, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tc.wantHost || port != tc.wantPort {
				t.Errorf("got %s:%d, want %s:%d", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "localhost", "::1", "[::1]"} {
		if !IsLoopbackHost(host) {
			t.Errorf("%q should count as loopback", host)
		}
	}
	for _, host := range []string{"10.0.0.12", "192.168.1.20", "::ffff:10.0.0.12"} {
		if IsLoopbackHost(host) {
			t.Errorf("%q should not count as loopback", host)
		}
	}
}

func TestIsLocalRegistry(t *testing.T) {
	cases := []struct {
		address string
		env     string
		want    bool
	}{
		{address: "", env: "", want: true},
		{address: "", env: "10.0.0.11:8500", want: false},
		{address: "127.0.0.1:8500", want: true},
		{address: "localhost:8500", want: true},
		{address: "http://127.0.0.1:8500", want: true},
		{address: "10.0.0.11:8500", want: false},
		{address: "consul:8500", want: false},
	}
	for _, tc := range cases {
		t.Setenv("CONSUL_HTTP_ADDR", tc.env)
		if got := IsLocalRegistry(tc.address); got != tc.want {
			t.Errorf("IsLocalRegistry(%q) with CONSUL_HTTP_ADDR=%q = %v, want %v", tc.address, tc.env, got, tc.want)
		}
	}
}

func TestServiceIDDefaults(t *testing.T) {
	cases := []struct {
		name string
		cfg  RegistrarConfig
		want string
	}{
		{
			name: "the advertised address makes the default unique per host",
			cfg:  RegistrarConfig{ServiceName: "sandboxd", ServiceAddr: "10.0.0.12", ServicePort: 27081},
			want: "sandboxd-10.0.0.12-27081",
		},
		{
			name: "without an address the old shape is kept",
			cfg:  RegistrarConfig{ServiceName: "sandboxd", ServicePort: 27081},
			want: "sandboxd-27081",
		},
		{
			name: "an explicit ID always wins",
			cfg: RegistrarConfig{
				ServiceName: "sandboxd",
				ServiceID:   "sandbox-primary",
				ServiceAddr: "10.0.0.12",
				ServicePort: 27081,
			},
			want: "sandbox-primary",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			applyRegistrarDefaults(&cfg)
			if cfg.ServiceID != tc.want {
				t.Errorf("ServiceID = %q, want %q", cfg.ServiceID, tc.want)
			}
		})
	}
}
