package discovery

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/hashicorp/mdns"
)

const serviceType = "_anthem._tcp"

// Advertiser broadcasts an Anthem instance on the local network via mDNS.
// Prism clients discover running instances by watching for _anthem._tcp.local.
type Advertiser struct {
	project string
	port    int
	repo    string
	logger  *slog.Logger
	server  *mdns.Server
}

func NewAdvertiser(project string, port int, repo string, logger *slog.Logger) *Advertiser {
	if logger == nil {
		logger = slog.Default()
	}
	return &Advertiser{
		project: project,
		port:    port,
		repo:    repo,
		logger:  logger,
	}
}

// Start begins advertising this Anthem instance via mDNS.
// TXT records carry project name, port, and repo for Prism discovery.
func (a *Advertiser) Start() error {
	host, err := os.Hostname()
	if err != nil {
		host = "anthem-host"
	}
	if !strings.HasSuffix(host, ".") {
		host += "."
	}

	info := []string{
		fmt.Sprintf("project=%s", a.project),
		fmt.Sprintf("port=%d", a.port),
		fmt.Sprintf("repo=%s", a.repo),
	}

	service, err := mdns.NewMDNSService(
		a.project,
		serviceType,
		"",
		host,
		a.port,
		[]net.IP{},
		info,
	)
	if err != nil {
		return fmt.Errorf("creating mDNS service: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return fmt.Errorf("starting mDNS server: %w", err)
	}

	a.server = server
	a.logger.Info("mDNS advertisement started",
		"service", serviceType,
		"project", a.project,
		"port", a.port,
	)
	return nil
}

// Stop deregisters the mDNS advertisement.
func (a *Advertiser) Stop() {
	if a.server != nil {
		a.server.Shutdown()
		a.logger.Info("mDNS advertisement stopped", "project", a.project)
	}
}
