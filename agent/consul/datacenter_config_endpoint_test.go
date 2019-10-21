package consul

import (
	"os"
	"testing"
	"time"

	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/hashicorp/consul/testrpc"
	"github.com/hashicorp/consul/types"
	uuid "github.com/hashicorp/go-uuid"
	msgpackrpc "github.com/hashicorp/net-rpc-msgpackrpc"
	"github.com/stretchr/testify/require"
)

func TestDatacenterConfig_Apply(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServer(t)
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()
	codec := rpcClient(t, s1)
	defer codec.Close()

	testrpc.WaitForLeader(t, s1.RPC, "dc1")

	dir2, s2 := testServerWithConfig(t, func(c *Config) {
		c.Datacenter = "dc2"
		c.PrimaryDatacenter = "dc1"
	})
	defer os.RemoveAll(dir2)
	defer s2.Shutdown()
	codec2 := rpcClient(t, s2)
	defer codec2.Close()

	testrpc.WaitForLeader(t, s2.RPC, "dc2")
	joinWAN(t, s2, s1)
	// wait for cross-dc queries to work
	testrpc.WaitForLeader(t, s2.RPC, "dc1")

	// update the primary with data from a secondary by way of request forwarding
	args := structs.DatacenterConfigRequest{
		Config: &structs.DatacenterConfig{
			Datacenter: "dc-test1",
			MeshGateways: []structs.CheckServiceNode{
				newTestMeshGatewayNode(
					"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
				),
				newTestMeshGatewayNode(
					"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
				),
			},
			UpdatedAt: time.Now().UTC(),
		},
	}
	out := false
	require.NoError(t, msgpackrpc.CallWithCodec(codec2, "DatacenterConfig.Apply", &args, &out))
	require.True(t, out)

	// the previous RPC should not return until the primary has been updated but will return
	// before the secondary has the data.
	state := s1.fsm.State()
	_, dcConfig2, err := state.DatacenterConfigGet(nil, "dc-test1")
	require.NoError(t, err)
	require.NotNil(t, dcConfig2)
	dcConfig2.RaftIndex = structs.RaftIndex{} // zero these out so the equality works
	require.Equal(t, args.Config, dcConfig2)

	retry.Run(t, func(r *retry.R) {
		// wait for replication to happen
		state := s2.fsm.State()
		_, dcConfig2Again, err := state.DatacenterConfigGet(nil, "dc-test1")
		require.NoError(r, err)
		require.NotNil(r, dcConfig2Again)

		// this test is not testing that the datacenter configs that are
		// replicated are correct as that's done elsewhere.
	})

	updated := &structs.DatacenterConfig{
		Datacenter: "dc-test1",
		MeshGateways: []structs.CheckServiceNode{
			newTestMeshGatewayNode(
				"dc-test1", "gateway3", "9.9.9.9", 7777, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
		},
		UpdatedAt: time.Now().UTC(),
	}

	args = structs.DatacenterConfigRequest{
		Config: updated,
	}

	out = false
	require.NoError(t, msgpackrpc.CallWithCodec(codec2, "DatacenterConfig.Apply", &args, &out))
	require.True(t, out)

	state = s1.fsm.State()
	_, dcConfig2, err = state.DatacenterConfigGet(nil, "dc-test1")
	require.NoError(t, err)
	require.NotNil(t, dcConfig2)

	dcConfig2.RaftIndex = structs.RaftIndex{} // zero these out so the equality works
	require.Equal(t, updated, dcConfig2)
}

func TestDatacenterConfig_Apply_ACLDeny(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServerWithConfig(t, func(c *Config) {
		c.ACLDatacenter = "dc1"
		c.ACLsEnabled = true
		c.ACLMasterToken = "root"
		c.ACLDefaultPolicy = "deny"
	})
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()

	testrpc.WaitForTestAgent(t, s1.RPC, "dc1")

	codec := rpcClient(t, s1)
	defer codec.Close()

	// Create the ACL tokens
	opReadToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `operator = "read"`)
	require.NoError(t, err)

	opWriteToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `operator = "write"`)
	require.NoError(t, err)

	expected := &structs.DatacenterConfig{
		Datacenter: "dc-test1",
		MeshGateways: []structs.CheckServiceNode{
			newTestMeshGatewayNode(
				"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
			newTestMeshGatewayNode(
				"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
		},
		UpdatedAt: time.Now().UTC(),
	}

	{ // This should fail since we don't have write perms.
		args := structs.DatacenterConfigRequest{
			Datacenter:   "dc1",
			Config:       expected,
			WriteRequest: structs.WriteRequest{Token: opReadToken.SecretID},
		}
		out := false
		err := msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Apply", &args, &out)
		if !acl.IsErrPermissionDenied(err) {
			t.Fatalf("err: %v", err)
		}
	}

	{ // This should work.
		args := structs.DatacenterConfigRequest{
			Datacenter:   "dc1",
			Config:       expected,
			WriteRequest: structs.WriteRequest{Token: opWriteToken.SecretID},
		}
		out := false
		require.NoError(t, msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Apply", &args, &out))
	}

	// the previous RPC should not return until the primary has been updated but will return
	// before the secondary has the data.
	state := s1.fsm.State()
	_, got, err := state.DatacenterConfigGet(nil, "dc-test1")
	require.NoError(t, err)
	require.NotNil(t, got)
	got.RaftIndex = structs.RaftIndex{} // zero these out so the equality works
	require.Equal(t, expected, got)
}

func TestDatacenterConfig_Get(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServer(t)
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()

	testrpc.WaitForTestAgent(t, s1.RPC, "dc1")

	codec := rpcClient(t, s1)
	defer codec.Close()

	expected := &structs.DatacenterConfig{
		Datacenter: "dc-test1",
		MeshGateways: []structs.CheckServiceNode{
			newTestMeshGatewayNode(
				"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
			newTestMeshGatewayNode(
				"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
		},
		UpdatedAt: time.Now().UTC(),
	}

	state := s1.fsm.State()
	require.NoError(t, state.DatacenterConfigSet(1, expected))

	args := structs.DatacenterConfigQuery{
		Datacenter:       "dc-test1",
		TargetDatacenter: "dc1",
	}
	var out structs.DatacenterConfigResponse
	require.NoError(t, msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Get", &args, &out))

	require.Equal(t, expected, out.Config)
}

func TestDatacenterConfig_Get_ACLDeny(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServerWithConfig(t, func(c *Config) {
		c.ACLDatacenter = "dc1"
		c.ACLsEnabled = true
		c.ACLMasterToken = "root"
		c.ACLDefaultPolicy = "deny"
	})
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()

	testrpc.WaitForTestAgent(t, s1.RPC, "dc1")

	codec := rpcClient(t, s1)
	defer codec.Close()

	// Create the ACL tokens
	nadaToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `
	service "foo" { policy = "write" }`)
	require.NoError(t, err)

	opReadToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `
	operator = "read"`)
	require.NoError(t, err)

	// create some dummy stuff to look up
	expected := &structs.DatacenterConfig{
		Datacenter: "dc-test1",
		MeshGateways: []structs.CheckServiceNode{
			newTestMeshGatewayNode(
				"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
			newTestMeshGatewayNode(
				"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
		},
		UpdatedAt: time.Now().UTC(),
	}

	state := s1.fsm.State()
	require.NoError(t, state.DatacenterConfigSet(1, expected))

	{ // This should fail
		args := structs.DatacenterConfigQuery{
			Datacenter:       "dc-test1",
			TargetDatacenter: "dc1",
			QueryOptions:     structs.QueryOptions{Token: nadaToken.SecretID},
		}
		var out structs.DatacenterConfigResponse
		err := msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Get", &args, &out)
		if !acl.IsErrPermissionDenied(err) {
			t.Fatalf("err: %v", err)
		}
	}

	{ // This should work
		args := structs.DatacenterConfigQuery{
			Datacenter:       "dc-test1",
			TargetDatacenter: "dc1",
			QueryOptions:     structs.QueryOptions{Token: opReadToken.SecretID},
		}
		var out structs.DatacenterConfigResponse
		require.NoError(t, msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Get", &args, &out))

		require.Equal(t, expected, out.Config)
	}
}

func TestDatacenterConfig_List(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServer(t)
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()

	testrpc.WaitForTestAgent(t, s1.RPC, "dc1")

	codec := rpcClient(t, s1)
	defer codec.Close()

	// create some dummy data
	expected := structs.IndexedDatacenterConfigs{
		Configs: []*structs.DatacenterConfig{
			{
				Datacenter: "dc-test1",
				MeshGateways: []structs.CheckServiceNode{
					newTestMeshGatewayNode(
						"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
				},
				UpdatedAt: time.Now().UTC(),
			},
			{
				Datacenter: "dc-test2",
				MeshGateways: []structs.CheckServiceNode{
					newTestMeshGatewayNode(
						"dc-test2", "gateway1", "5.6.7.8", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						"dc-test2", "gateway2", "8.7.6.5", 1111, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
				},
				UpdatedAt: time.Now().UTC(),
			},
		},
	}

	state := s1.fsm.State()
	require.NoError(t, state.DatacenterConfigSet(1, expected.Configs[0]))
	require.NoError(t, state.DatacenterConfigSet(2, expected.Configs[1]))

	args := structs.DatacenterConfigQuery{
		Datacenter: "dc1",
	}
	var out structs.IndexedDatacenterConfigs
	require.NoError(t, msgpackrpc.CallWithCodec(codec, "DatacenterConfig.List", &args, &out))

	// exclude dc1 which is written by a background routine on the leader and is not relevant
	out.Configs = omitDatacenterConfig(out.Configs, "dc1")

	require.Equal(t, expected.Configs, out.Configs)
}

func omitDatacenterConfig(all []*structs.DatacenterConfig, omit string) []*structs.DatacenterConfig {
	var out []*structs.DatacenterConfig
	for _, config := range all {
		if config.Datacenter != omit {
			out = append(out, config)
		}
	}
	return out
}

func TestDatacenterConfig_List_ACLDeny(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServerWithConfig(t, func(c *Config) {
		c.ACLDatacenter = "dc1"
		c.ACLsEnabled = true
		c.ACLMasterToken = "root"
		c.ACLDefaultPolicy = "deny"
	})
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()

	testrpc.WaitForTestAgent(t, s1.RPC, "dc1")

	codec := rpcClient(t, s1)
	defer codec.Close()

	// Create the ACL tokens
	nadaToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `
	service "foo" { policy = "write" }`)
	require.NoError(t, err)

	opReadToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `
	operator = "read"`)
	require.NoError(t, err)

	// create some dummy data
	expected := structs.IndexedDatacenterConfigs{
		Configs: []*structs.DatacenterConfig{
			{
				Datacenter: "dc-test1",
				MeshGateways: []structs.CheckServiceNode{
					newTestMeshGatewayNode(
						"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
				},
				UpdatedAt: time.Now().UTC(),
			},
			{
				Datacenter: "dc-test2",
				MeshGateways: []structs.CheckServiceNode{
					newTestMeshGatewayNode(
						"dc-test2", "gateway1", "5.6.7.8", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
					newTestMeshGatewayNode(
						"dc-test2", "gateway2", "8.7.6.5", 1111, map[string]string{"wanfed": "1"}, api.HealthPassing,
					),
				},
				UpdatedAt: time.Now().UTC(),
			},
		},
	}

	state := s1.fsm.State()
	require.NoError(t, state.DatacenterConfigSet(1, expected.Configs[0]))
	require.NoError(t, state.DatacenterConfigSet(2, expected.Configs[1]))

	{ // This should not work
		args := structs.DatacenterConfigQuery{
			Datacenter:   "dc1",
			QueryOptions: structs.QueryOptions{Token: nadaToken.SecretID},
		}
		var out structs.IndexedDatacenterConfigs
		err := msgpackrpc.CallWithCodec(codec, "DatacenterConfig.List", &args, &out)
		if !acl.IsErrPermissionDenied(err) {
			t.Fatalf("err: %v", err)
		}
	}

	{ // This should work
		args := structs.DatacenterConfigQuery{
			Datacenter:   "dc1",
			QueryOptions: structs.QueryOptions{Token: opReadToken.SecretID},
		}
		var out structs.IndexedDatacenterConfigs
		require.NoError(t, msgpackrpc.CallWithCodec(codec, "DatacenterConfig.List", &args, &out))

		// exclude dc1 which is written by a background routine on the leader and is not relevant
		out.Configs = omitDatacenterConfig(out.Configs, "dc1")

		require.Equal(t, expected.Configs, out.Configs)
	}
}

func TestDatacenterConfig_Delete(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServer(t)
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()
	codec := rpcClient(t, s1)
	defer codec.Close()

	testrpc.WaitForLeader(t, s1.RPC, "dc1")

	dir2, s2 := testServerWithConfig(t, func(c *Config) {
		c.Datacenter = "dc2"
		c.PrimaryDatacenter = "dc1"
	})
	defer os.RemoveAll(dir2)
	defer s2.Shutdown()
	codec2 := rpcClient(t, s2)
	defer codec2.Close()

	testrpc.WaitForLeader(t, s2.RPC, "dc2")
	joinWAN(t, s2, s1)
	// wait for cross-dc queries to work
	testrpc.WaitForLeader(t, s2.RPC, "dc1")

	// Create a dummy dc config in the state store to look up.
	dcConfig := &structs.DatacenterConfig{
		Datacenter: "dc-test1",
		MeshGateways: []structs.CheckServiceNode{
			newTestMeshGatewayNode(
				"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
			newTestMeshGatewayNode(
				"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
		},
		UpdatedAt: time.Now().UTC(),
	}

	state := s1.fsm.State()
	require.NoError(t, state.DatacenterConfigSet(1, dcConfig))

	// Verify it's there
	_, existing, err := state.DatacenterConfigGet(nil, "dc-test1")
	require.NoError(t, err)
	require.Equal(t, dcConfig, existing)

	retry.Run(t, func(r *retry.R) {
		// wait for it to be replicated into the secondary dc
		state := s2.fsm.State()
		_, dcConfig2Again, err := state.DatacenterConfigGet(nil, "dc-test1")
		require.NoError(r, err)
		require.NotNil(r, dcConfig2Again)
	})

	// send the delete request to dc2 - it should get forwarded to dc1.
	args := structs.DatacenterConfigRequest{
		Config: dcConfig,
	}
	out := false
	require.NoError(t, msgpackrpc.CallWithCodec(codec2, "DatacenterConfig.Delete", &args, &out))

	// Verify the entry was deleted.
	_, existing, err = s1.fsm.State().DatacenterConfigGet(nil, "dc-test1")
	require.NoError(t, err)
	require.Nil(t, existing)

	// verify it gets deleted from the secondary too
	retry.Run(t, func(r *retry.R) {
		_, existing, err := s2.fsm.State().DatacenterConfigGet(nil, "dc-test1")
		require.NoError(r, err)
		require.Nil(r, existing)
	})
}

func TestDatacenterConfig_Delete_ACLDeny(t *testing.T) {
	t.Parallel()

	dir1, s1 := testServerWithConfig(t, func(c *Config) {
		c.ACLDatacenter = "dc1"
		c.ACLsEnabled = true
		c.ACLMasterToken = "root"
		c.ACLDefaultPolicy = "deny"
	})
	defer os.RemoveAll(dir1)
	defer s1.Shutdown()

	testrpc.WaitForLeader(t, s1.RPC, "dc1")

	codec := rpcClient(t, s1)
	defer codec.Close()

	// Create the ACL tokens
	opReadToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `
operator = "read"`)
	require.NoError(t, err)

	opWriteToken, err := upsertTestTokenWithPolicyRules(codec, "root", "dc1", `
operator = "write"`)
	require.NoError(t, err)

	// Create a dummy dc config in the state store to look up.
	dcConfig := &structs.DatacenterConfig{
		Datacenter: "dc-test1",
		MeshGateways: []structs.CheckServiceNode{
			newTestMeshGatewayNode(
				"dc-test1", "gateway1", "1.2.3.4", 5555, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
			newTestMeshGatewayNode(
				"dc-test1", "gateway2", "4.3.2.1", 9999, map[string]string{"wanfed": "1"}, api.HealthPassing,
			),
		},
		UpdatedAt: time.Now().UTC(),
	}

	state := s1.fsm.State()
	require.NoError(t, state.DatacenterConfigSet(1, dcConfig))

	{ // This should not work
		args := structs.DatacenterConfigRequest{
			Config:       dcConfig,
			WriteRequest: structs.WriteRequest{Token: opReadToken.SecretID},
		}
		out := false
		err := msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Delete", &args, &out)
		if !acl.IsErrPermissionDenied(err) {
			t.Fatalf("err: %v", err)
		}
	}

	{ // This should work
		args := structs.DatacenterConfigRequest{
			Config:       dcConfig,
			WriteRequest: structs.WriteRequest{Token: opWriteToken.SecretID},
		}
		out := false
		require.NoError(t, msgpackrpc.CallWithCodec(codec, "DatacenterConfig.Delete", &args, &out))
	}

	// Verify the entry was deleted.
	_, existing, err := state.DatacenterConfigGet(nil, "dc-test1")
	require.NoError(t, err)
	require.Nil(t, existing)
}

func newTestGatewayList(
	ip1 string, port1 int, meta1 map[string]string,
	ip2 string, port2 int, meta2 map[string]string,
) structs.CheckServiceNodes {
	return []structs.CheckServiceNode{
		{
			Node: &structs.Node{
				ID:         "664bac9f-4de7-4f1b-ad35-0e5365e8f329",
				Node:       "gateway1",
				Datacenter: "dc1",
				Address:    ip1,
			},
			Service: &structs.NodeService{
				ID:      "mesh-gateway",
				Service: "mesh-gateway",
				Port:    port1,
				Meta:    meta1,
			},
			Checks: []*structs.HealthCheck{
				{
					Name:      "web connectivity",
					Status:    api.HealthPassing,
					ServiceID: "mesh-gateway",
				},
			},
		},
		{
			Node: &structs.Node{
				ID:         "3fb9a696-8209-4eee-a1f7-48600deb9716",
				Node:       "gateway2",
				Datacenter: "dc1",
				Address:    ip2,
			},
			Service: &structs.NodeService{
				ID:      "mesh-gateway",
				Service: "mesh-gateway",
				Port:    port2,
				Meta:    meta2,
			},
			Checks: []*structs.HealthCheck{
				{
					Name:      "web connectivity",
					Status:    api.HealthPassing,
					ServiceID: "mesh-gateway",
				},
			},
		},
	}
}

func newTestMeshGatewayNode(
	datacenter, node string,
	ip string,
	port int,
	meta map[string]string,
	healthStatus string,
) structs.CheckServiceNode {
	id, err := uuid.GenerateUUID()
	if err != nil {
		panic(err)
	}

	return structs.CheckServiceNode{
		Node: &structs.Node{
			ID:         types.NodeID(id),
			Node:       node,
			Datacenter: datacenter,
			Address:    ip,
		},
		Service: &structs.NodeService{
			ID:      "mesh-gateway",
			Service: "mesh-gateway",
			Kind:    structs.ServiceKindMeshGateway,
			Port:    port,
			Meta:    meta,
		},
		Checks: []*structs.HealthCheck{
			{
				Name:      "web connectivity",
				Status:    healthStatus,
				ServiceID: "mesh-gateway",
			},
		},
	}
}
