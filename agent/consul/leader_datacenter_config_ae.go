package consul

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/consul/agent/consul/state"
	"github.com/hashicorp/consul/agent/structs"
	memdb "github.com/hashicorp/go-memdb"
)

var (
	datacenterConfigAntiEntropyRefreshTimeout = 10 * time.Minute
)

func (s *Server) startDatacenterConfigAntiEntropy() {
	s.leaderRoutineManager.Start(datacenterConfigAntiEntropyRoutineName, s.datacenterConfigAntiEntropySync)
}

func (s *Server) stopDatacenterConfigAntiEntropy() {
	s.leaderRoutineManager.Stop(datacenterConfigAntiEntropyRoutineName)
}

func (s *Server) datacenterConfigAntiEntropySync(ctx context.Context) error {
	var lastFetchIndex uint64

	retryLoopBackoff(ctx.Done(), func() error {
		idx, err := s.datacenterConfigAntiEntropyMaybeSync(lastFetchIndex)
		if err != nil {
			return err
		}

		lastFetchIndex = idx
		return nil
	}, func(err error) {
		s.logger.Printf("[ERR] leader: error performing anti-entropy sync of datacenter config: %v", err)
	})

	return nil
}

func (s *Server) datacenterConfigAntiEntropyMaybeSync(lastFetchIndex uint64) (uint64, error) {
	// TODO(rb): make this aware of the up-to-date-ness of the dcconfig replicator

	queryOpts := &structs.QueryOptions{
		MaxQueryTime:      datacenterConfigAntiEntropyRefreshTimeout,
		MinQueryIndex:     lastFetchIndex,
		RequireConsistent: true,
	}

	idx, prev, curr, err := s.fetchDatacenterConfigAntiEntropyDetails(queryOpts)
	if err != nil {
		return 0, err
	}

	if prev != nil && prev.IsSame(curr) {
		s.logger.Printf("[DEBUG] leader: datacenter config anti-entropy sync skipped; already up to date")
		return idx, nil
	}

	curr.UpdatedAt = time.Now().UTC()

	args := structs.DatacenterConfigRequest{
		Config: curr,
	}
	ignored := false
	if err := s.forwardDC("DatacenterConfig.Apply", s.config.PrimaryDatacenter, &args, &ignored); err != nil {
		return 0, fmt.Errorf("error performing datacenter config anti-entropy sync: %v", err)
	}

	s.logger.Printf("[INFO] leader: datacenter config anti-entropy synced")

	return idx, nil
}

func (s *Server) fetchDatacenterConfigAntiEntropyDetails(
	queryOpts *structs.QueryOptions,
) (uint64, *structs.DatacenterConfig, *structs.DatacenterConfig, error) {
	var (
		prevConfig, currConfig *structs.DatacenterConfig
		queryMeta              structs.QueryMeta
	)
	err := s.blockingQuery(
		queryOpts,
		&queryMeta,
		func(ws memdb.WatchSet, state *state.Store) error {
			// Get the existing stored version of this config that has replicated down.
			// We could phone home to get this but that would incur extra WAN traffic
			// when we already have enough information locally to figure it out
			// (assuming that our replicator is still functioning).
			idx1, prev, err := state.DatacenterConfigGet(ws, s.config.Datacenter)
			if err != nil {
				return err
			}

			// Fetch our current list of all mesh gateways.
			idx2, raw, err := state.ServiceDump(ws, structs.ServiceKindMeshGateway, true)
			if err != nil {
				return err
			}

			curr := &structs.DatacenterConfig{
				Datacenter:   s.config.Datacenter,
				MeshGateways: raw,
			}

			if idx2 > idx1 {
				queryMeta.Index = idx2
			} else {
				queryMeta.Index = idx1
			}

			prevConfig = prev
			currConfig = curr

			return nil
		})
	if err != nil {
		return 0, nil, nil, err
	}

	return queryMeta.Index, prevConfig, currConfig, nil
}
