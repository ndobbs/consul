package agent

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/consul/lib"
	discover "github.com/hashicorp/go-discover"
	discoverk8s "github.com/hashicorp/go-discover/provider/k8s"
)

func (a *Agent) retryJoinLAN() {
	r := &retryJoiner{
		variant:     retryJoinSerfVariant,
		cluster:     "LAN",
		addrs:       a.config.RetryJoinLAN,
		maxAttempts: a.config.RetryJoinMaxAttemptsLAN,
		interval:    a.config.RetryJoinIntervalLAN,
		join:        a.JoinLAN,
		logger:      a.logger,
	}
	if err := r.retryJoin(); err != nil {
		a.retryJoinCh <- err
	}
}

func (a *Agent) retryJoinWAN() {
	// TODO: how to intersect go-discover with the gateway stuff? Maybe we change this to use a composite syntax
	// TODO: how to force a rejoin after it accidentally just joins itself?
	r := &retryJoiner{
		variant:      retryJoinSerfVariant,
		cluster:      "WAN",
		addrs:        a.config.RetryJoinWAN,
		maxAttempts:  a.config.RetryJoinMaxAttemptsWAN,
		interval:     a.config.RetryJoinIntervalWAN,
		join:         a.JoinWAN,
		logger:       a.logger,
		notifyRejoin: a.notifyRejoinWANCh,
	}
	if err := r.retryJoin(); err != nil {
		a.retryJoinCh <- err
	}
}

func (a *Agent) refreshPrimaryGatewayFallbackAddresses() {
	comboFn := func(addrs []string) (int, error) {
		n, err := a.RefreshPrimaryGatewayFallbackAddresses(addrs)
		if err == nil {
			select {
			case a.notifyRejoinWANCh <- struct{}{}:
			default:
				// Don't block if it's already been notified.
			}
		}
		return n, err
	}

	r := &retryJoiner{
		variant: retryJoinMeshGatewayVariant,
		cluster: "primary",
		addrs:   a.config.PrimaryGateways,
		// TODO: make these configurable
		maxAttempts: 0,                // s.config.RetryJoinMaxAttemptsWAN,
		interval:    30 * time.Second, // s.config.RetryJoinIntervalWAN,
		join:        comboFn,
		logger:      a.logger,
	}
	if err := r.retryJoin(); err != nil {
		a.primaryMeshGatewayRefreshCh <- err
	}
}

func newDiscover() (*discover.Discover, error) {
	providers := make(map[string]discover.Provider)
	for k, v := range discover.Providers {
		providers[k] = v
	}
	providers["k8s"] = &discoverk8s.Provider{}

	return discover.New(
		discover.WithUserAgent(lib.UserAgent()),
		discover.WithProviders(providers),
	)
}

func retryJoinAddrs(disco *discover.Discover, variant, cluster string, retryJoin []string, logger *log.Logger) []string {
	addrs := []string{}
	if disco == nil {
		return addrs
	}
	for _, addr := range retryJoin {
		switch {
		case strings.Contains(addr, "provider="):
			servers, err := disco.Addrs(addr, logger)
			if err != nil {
				if logger != nil {
					logger.Printf("[ERR] agent: Cannot discover %s %s: %s", cluster, addr, err)
				}
			} else {
				addrs = append(addrs, servers...)
				if logger != nil {
					if variant == retryJoinMeshGatewayVariant {
						logger.Printf("[INFO] agent: Discovered %q mesh gateways: %s", cluster, strings.Join(servers, " "))
					} else {
						logger.Printf("[INFO] agent: Discovered %s servers: %s", cluster, strings.Join(servers, " "))
					}
				}
			}

		default:
			addrs = append(addrs, addr)
		}
	}

	return addrs
}

const (
	retryJoinSerfVariant        = "serf"
	retryJoinMeshGatewayVariant = "mesh-gateway"
)

// retryJoiner is used to handle retrying a join until it succeeds or all
// retries are exhausted.
type retryJoiner struct {
	// variant is either "serf" or "mesh-gateway" and just adjusts the log messaging
	// emitted
	variant string

	// cluster is the name of the serf cluster, e.g. "LAN" or "WAN".
	cluster string

	// addrs is the list of servers or go-discover configurations
	// to join with.
	addrs []string

	// maxAttempts is the number of join attempts before giving up.
	maxAttempts int

	// interval is the time between two join attempts.
	interval time.Duration

	notifyRejoin chan struct{}

	// join adds the discovered or configured servers to the given
	// serf cluster.
	join func([]string) (int, error)

	// logger is the agent logger. Log messages should contain the
	// "agent: " prefix.
	logger *log.Logger
}

func (r *retryJoiner) retryJoin() error {
	if len(r.addrs) == 0 {
		return nil
	}

	disco, err := newDiscover()
	if err != nil {
		return err
	}

	if r.variant == retryJoinMeshGatewayVariant {
		r.logger.Printf("[INFO] agent: Refresh mesh gateways for %s is supported for: %s", r.cluster, strings.Join(disco.Names(), " "))
		r.logger.Printf("[INFO] agent: Refreshing %s mesh gateways...", r.cluster)
	} else {
		r.logger.Printf("[INFO] agent: Retry join %s is supported for: %s", r.cluster, strings.Join(disco.Names(), " "))
		r.logger.Printf("[INFO] agent: Joining %s cluster...", r.cluster)
	}

	attempt := 0
	for {
		addrs := retryJoinAddrs(disco, r.variant, r.cluster, r.addrs, r.logger)
		if r.variant == retryJoinMeshGatewayVariant { // TODO
			r.logger.Printf("[DEBUG] agent retryJoin looping for MGW named %q", r.cluster)
		} else { // TODO
			r.logger.Printf("[DEBUG] agent retryJoin looping for JOIN type %q", r.cluster)
		}
		if len(addrs) > 0 {
			n, err := r.join(addrs)
			if err == nil {
				if r.variant == retryJoinMeshGatewayVariant {
					r.logger.Printf("[INFO] agent: Refreshing %s mesh gateways completed.", r.cluster)
				} else {
					r.logger.Printf("[INFO] agent: Join %s completed. Synced with %d initial agents", r.cluster, n)
				}
				return nil
			}
		} else if len(addrs) == 0 {
			if r.variant == retryJoinMeshGatewayVariant {
				err = fmt.Errorf("No mesh gateways found")
			} else {
				err = fmt.Errorf("No servers to join")
			}
		}

		attempt++
		if r.maxAttempts > 0 && attempt > r.maxAttempts {
			if r.variant == retryJoinMeshGatewayVariant {
				return fmt.Errorf("agent: max refresh of %s mesh gateways retry exhausted, exiting", r.cluster)
			} else {
				return fmt.Errorf("agent: max join %s retry exhausted, exiting", r.cluster)
			}
		}

		if r.variant == retryJoinMeshGatewayVariant {
			r.logger.Printf("[WARN] agent: Refresh of %s mesh gateways failed: %v, retrying in %v", r.cluster, err, r.interval)
		} else {
			r.logger.Printf("[WARN] agent: Join %s failed: %v, retrying in %v", r.cluster, err, r.interval)
		}

		select {
		case <-time.After(r.interval):
		case <-r.notifyRejoin:
			//TODO
			r.logger.Printf("[DEBUG] agent: WOKE UP %q", r.cluster)
		}
	}
}
