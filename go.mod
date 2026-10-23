module github.com/GoogleCloudPlatform/google-guest-agent

go 1.26.2

toolchain go1.27.2

require (
	cloud.google.com/go/compute/metadata v0.10.0
	cloud.google.com/go/storage v1.69.0
	github.com/GoogleCloudPlatform/agentcommunication_client v0.0.0-20261013221819-6db59e76b4ca
	github.com/GoogleCloudPlatform/galog v0.0.0-20260610212417-8f44113b5479
	github.com/Microsoft/go-winio v0.6.4
	github.com/fsnotify/fsnotify v1.10.1
	github.com/google/go-cmp v0.7.0
	github.com/google/go-tpm v0.9.8
	github.com/google/go-tpm-tools v0.4.11
	github.com/google/tink/go v1.7.0
	github.com/google/uuid v1.6.0
	github.com/googleapis/gax-go/v2 v2.26.2
	github.com/miekg/dns v1.1.73
	github.com/prometheus/procfs v0.14.0
	github.com/safchain/ethtool v0.4.1
	github.com/shirou/gopsutil/v3 v3.24.5
	github.com/spf13/cobra v1.10.2
	go.bug.st/serial v1.8.0
	golang.org/x/crypto v0.58.0
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e
	golang.org/x/sys v0.49.0
	google.golang.org/api v0.305.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/ini.v1 v1.67.3
	gopkg.in/yaml.v3 v3.0.1
	software.sslmate.com/src/go-pkcs12 v0.7.4
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.1 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.0 // indirect
	cloud.google.com/go/iam v1.14.0 // indirect
	cloud.google.com/go/logging v1.20.0 // indirect
	cloud.google.com/go/longrunning v1.3.0 // indirect
	cloud.google.com/go/monitoring v1.31.0 // indirect
	github.com/GoogleCloudPlatform/confidential-space/server v0.0.0-20261015094924-dba3d44e282b // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.62.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.62.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.40.0 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/go-configfs-tsm v0.3.3 // indirect
	github.com/google/go-sev-guest v0.15.0 // indirect
	github.com/google/go-tdx-guest v0.3.2-0.20250814004405-ffb0869e6f4d // indirect
	github.com/google/logger v1.1.2 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.23 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20260802145828-341c2f0c90b5 // indirect
	github.com/mdlayher/socket v0.8.0 // indirect
	github.com/mdlayher/vsock v1.3.0 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/power-devops/perfstat v0.0.0-20260916203055-22a1a467d9f0 // indirect
	github.com/shoenig/go-m1cpu v0.2.4 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/spiffe/go-spiffe/v2 v2.9.0 // indirect
	github.com/tklauser/go-sysconf v0.4.0 // indirect
	github.com/tklauser/numcpus v0.12.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.47.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
)
