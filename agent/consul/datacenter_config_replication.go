package consul

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/consul/agent/structs"
)

func datacenterConfigSort(configs []*structs.DatacenterConfig) {
	sort.Slice(configs, func(i, j int) bool {
		return configs[i].Datacenter < configs[j].Datacenter
	})
}

func diffDatacenterConfigs(local []*structs.DatacenterConfig, remote []*structs.DatacenterConfig, lastRemoteIndex uint64) ([]*structs.DatacenterConfig, []*structs.DatacenterConfig) {
	datacenterConfigSort(local)
	datacenterConfigSort(remote)

	var deletions []*structs.DatacenterConfig
	var updates []*structs.DatacenterConfig
	var localIdx int
	var remoteIdx int
	for localIdx, remoteIdx = 0, 0; localIdx < len(local) && remoteIdx < len(remote); {
		if local[localIdx].Datacenter == remote[remoteIdx].Datacenter {
			// config is in both the local and remote state - need to check raft indices
			if remote[remoteIdx].ModifyIndex > lastRemoteIndex {
				updates = append(updates, remote[remoteIdx])
			}
			// increment both indices when equal
			localIdx += 1
			remoteIdx += 1
		} else if local[localIdx].Datacenter < remote[remoteIdx].Datacenter {
			// config no longer in remoted state - needs deleting
			deletions = append(deletions, local[localIdx])

			// increment just the local index
			localIdx += 1
		} else {
			// local state doesn't have this config - needs updating
			updates = append(updates, remote[remoteIdx])

			// increment just the remote index
			remoteIdx += 1
		}
	}

	for ; localIdx < len(local); localIdx += 1 {
		deletions = append(deletions, local[localIdx])
	}

	for ; remoteIdx < len(remote); remoteIdx += 1 {
		updates = append(updates, remote[remoteIdx])
	}

	return deletions, updates
}

func (s *Server) reconcileLocalDatacenterConfig(ctx context.Context, configs []*structs.DatacenterConfig, op structs.DatacenterConfigOp) (bool, error) {
	ticker := time.NewTicker(time.Second / time.Duration(s.config.DatacenterConfigReplicationApplyLimit))
	defer ticker.Stop()

	for i, config := range configs {
		req := structs.DatacenterConfigRequest{
			Op:         op,
			Datacenter: s.config.Datacenter,
			Config:     config,
		}

		resp, err := s.raftApply(structs.DatacenterConfigRequestType, &req)
		if err != nil {
			return false, fmt.Errorf("Failed to apply datacenter config %s: %v", op, err)
		}
		if respErr, ok := resp.(error); ok && err != nil {
			return false, fmt.Errorf("Failed to apply datacenter config %s: %v", op, respErr)
		}

		if i < len(configs)-1 {
			select {
			case <-ctx.Done():
				return true, nil
			case <-ticker.C:
				// do nothing - ready for the next batch
			}
		}
	}

	return false, nil
}

func (s *Server) fetchDatacenterConfigs(lastRemoteIndex uint64) (*structs.IndexedDatacenterConfigs, error) {
	defer metrics.MeasureSince([]string{"leader", "replication", "datacenter-configs", "fetch"}, time.Now())

	req := structs.DCSpecificRequest{
		Datacenter: s.config.PrimaryDatacenter,
		QueryOptions: structs.QueryOptions{
			AllowStale:    true,
			MinQueryIndex: lastRemoteIndex,
			Token:         s.tokens.ReplicationToken(),
		},
	}

	var response structs.IndexedDatacenterConfigs
	if err := s.RPC("DatacenterConfig.List", &req, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

func (s *Server) replicateDatacenterConfig(ctx context.Context, lastRemoteIndex uint64) (uint64, bool, error) {
	remote, err := s.fetchDatacenterConfigs(lastRemoteIndex)
	if err != nil {
		return 0, false, fmt.Errorf("failed to retrieve remote datacenter configs: %v", err)
	}

	s.logger.Printf("[DEBUG] replication: finished fetching datacenter configs: %d", len(remote.Configs))

	// Need to check if we should be stopping. This will be common as the fetching process is a blocking
	// RPC which could have been hanging around for a long time and during that time leadership could
	// have been lost.
	select {
	case <-ctx.Done():
		return 0, true, nil
	default:
		// do nothing
	}

	// Measure everything after the remote query, which can block for long
	// periods of time. This metric is a good measure of how expensive the
	// replication process is.
	defer metrics.MeasureSince([]string{"leader", "replication", "datacenter-config", "apply"}, time.Now())

	_, local, err := s.fsm.State().DatacenterConfigList(nil)
	if err != nil {
		return 0, false, fmt.Errorf("failed to retrieve local datacenter configs: %v", err)
	}

	// If the remote index ever goes backwards, it's a good indication that
	// the remote side was rebuilt and we should do a full sync since we
	// can't make any assumptions about what's going on.
	//
	// Resetting lastRemoteIndex to 0 will work because we never consider local
	// raft indices. Instead we compare the raft modify index in the response object
	// with the lastRemoteIndex (only when we already have a datacenter config of the same name)
	// to determine if an update is needed. Resetting lastRemoteIndex to 0 then has the affect
	// of making us think all the local state is out of date and any matching configs should
	// still be updated.
	//
	// The lastRemoteIndex is not used when the entry exists either only in the local state or
	// only in the remote state. In those situations we need to either delete it or create it.
	if remote.QueryMeta.Index < lastRemoteIndex {
		s.logger.Printf("[WARN] replication: Datacenter Config replication remote index moved backwards (%d to %d), forcing a full Datacenter Config sync", lastRemoteIndex, remote.QueryMeta.Index)
		lastRemoteIndex = 0
	}

	s.logger.Printf("[DEBUG] replication: Datacenter Config replication - local: %d, remote: %d", len(local), len(remote.Configs))
	// Calculate the changes required to bring the state into sync and then
	// apply them.
	deletions, updates := diffDatacenterConfigs(local, remote.Configs, lastRemoteIndex)

	s.logger.Printf("[DEBUG] replication: Datacenter Config replication - deletions: %d, updates: %d", len(deletions), len(updates))

	if len(deletions) > 0 {
		s.logger.Printf("[DEBUG] replication: Datacenter Config replication - performing %d deletions", len(deletions))

		exit, err := s.reconcileLocalDatacenterConfig(ctx, deletions, structs.DatacenterConfigDelete)
		if exit {
			return 0, true, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("failed to delete local datacenter configs: %v", err)
		}
		s.logger.Printf("[DEBUG] replication: Datacenter Config replication - finished deletions")
	}

	if len(updates) > 0 {
		s.logger.Printf("[DEBUG] replication: Datacenter Config replication - performing %d updates", len(updates))
		exit, err := s.reconcileLocalDatacenterConfig(ctx, updates, structs.DatacenterConfigUpsert)
		if exit {
			return 0, true, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("failed to update local datacenter configs: %v", err)
		}
		s.logger.Printf("[DEBUG] replication: Datacenter Config replication - finished updates")
	}

	// Return the index we got back from the remote side, since we've synced
	// up with the remote state as of that index.
	return remote.QueryMeta.Index, false, nil
}
