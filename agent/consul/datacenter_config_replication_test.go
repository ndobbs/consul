package consul

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/hashicorp/consul/testrpc"
	"github.com/stretchr/testify/require"
)

func TestReplication_DatacenterConfigs(t *testing.T) {
	t.Parallel()
	dir1, s1 := testServerWithConfig(t, func(c *Config) {
		c.PrimaryDatacenter = "dc1"
	})
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()
	testrpc.WaitForLeader(t, s1.RPC, "dc1")
	client := rpcClient(t, s1)
	defer client.Close()

	dir2, s2 := testServerWithConfig(t, func(c *Config) {
		c.Datacenter = "dc2"
		c.PrimaryDatacenter = "dc1"
		c.DatacenterConfigReplicationRate = 100
		c.DatacenterConfigReplicationBurst = 100
		c.DatacenterConfigReplicationApplyLimit = 1000000
	})
	testrpc.WaitForLeader(t, s2.RPC, "dc2")
	defer os.RemoveAll(dir2)
	defer s2.Shutdown()

	// Try to join.
	joinWAN(t, s2, s1)
	testrpc.WaitForLeader(t, s1.RPC, "dc1")
	testrpc.WaitForLeader(t, s1.RPC, "dc2")

	// Create some new dc configs (weird because we're having dc1 update it for the other 50)
	var configs []*structs.DatacenterConfig
	for i := 0; i < 50; i++ {
		dc := fmt.Sprintf("alt-dc%d", i+1)
		ip1 := fmt.Sprintf("1.2.3.%d", i+1)
		ip2 := fmt.Sprintf("4.3.2.%d", i+1)
		arg := structs.DatacenterConfigRequest{
			Datacenter: "dc1",
			Op:         structs.DatacenterConfigUpsert,
			Config: &structs.DatacenterConfig{
				Datacenter: dc,
				MeshGateways: []structs.CheckServiceNode{
					newTestMeshGatewayNode(
						dc, "gateway1", ip1, 443, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						dc, "gateway2", ip2, 443, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
				},
				UpdatedAt: time.Now().UTC(),
			},
		}

		out := false
		require.NoError(t, s1.RPC("DatacenterConfig.Apply", &arg, &out))
		configs = append(configs, arg.Config)
	}

	checkSame := func(t *retry.R) error {
		_, remote, err := s1.fsm.State().DatacenterConfigList(nil)
		require.NoError(t, err)
		_, local, err := s2.fsm.State().DatacenterConfigList(nil)
		require.NoError(t, err)

		require.Len(t, local, len(remote))
		for i, _ := range remote {
			// zero out the raft data for future comparisons
			remote[i].RaftIndex = structs.RaftIndex{}
			local[i].RaftIndex = structs.RaftIndex{}
			require.Equal(t, remote[i], local[i])
		}
		return nil
	}

	// Wait for the replica to converge.
	retry.Run(t, func(r *retry.R) {
		checkSame(r)
	})

	// Update those configs
	for i := 0; i < 50; i++ {
		dc := fmt.Sprintf("alt-dc%d", i+1)
		ip1 := fmt.Sprintf("1.2.3.%d", i+1)
		ip2 := fmt.Sprintf("4.3.2.%d", i+1)
		ip3 := fmt.Sprintf("5.8.9.%d", i+1)
		arg := structs.DatacenterConfigRequest{
			Datacenter: "dc1",
			Op:         structs.DatacenterConfigUpsert,
			Config: &structs.DatacenterConfig{
				Datacenter: dc,
				MeshGateways: []structs.CheckServiceNode{
					newTestMeshGatewayNode(
						dc, "gateway1", ip1, 8443, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						dc, "gateway2", ip2, 8443, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						dc, "gateway3", ip3, 8443, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
				},
				UpdatedAt: time.Now().UTC(),
			},
		}

		out := false
		require.NoError(t, s1.RPC("DatacenterConfig.Apply", &arg, &out))
	}

	// Wait for the replica to converge.
	retry.Run(t, func(r *retry.R) {
		checkSame(r)
	})

	for _, config := range configs {
		arg := structs.DatacenterConfigRequest{
			Datacenter: "dc1",
			Op:         structs.DatacenterConfigDelete,
			Config:     config,
		}

		out := false
		require.NoError(t, s1.RPC("DatacenterConfig.Delete", &arg, &out))
	}

	// Wait for the replica to converge.
	retry.Run(t, func(r *retry.R) {
		checkSame(r)
	})
}
