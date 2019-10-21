package consul

import (
	"time"

	metrics "github.com/armon/go-metrics"
	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/consul/state"
	"github.com/hashicorp/consul/agent/structs"
	memdb "github.com/hashicorp/go-memdb"
)

// TODO: move these under Operator?

// TODO(docs)
type DatacenterConfig struct {
	srv *Server
}

func (c *DatacenterConfig) Apply(args *structs.DatacenterConfigRequest, reply *bool) error {
	// Ensure that all datacenter config writes go to the primary datacenter. These will then
	// be replicated to all the other datacenters.
	args.Datacenter = c.srv.config.PrimaryDatacenter

	if done, err := c.srv.forward("DatacenterConfig.Apply", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"datacenter_config", "apply"}, time.Now())

	// Fetch the ACL token, if any.
	rule, err := c.srv.ResolveToken(args.Token)
	if err != nil {
		return err
	}
	// TODO (namespaces) use actual ent authz context
	if rule != nil && rule.OperatorWrite(nil) != acl.Allow {
		return acl.ErrPermissionDenied
	}

	if args.Config.UpdatedAt.IsZero() {
		args.Config.UpdatedAt = time.Now().UTC()
	}

	args.Op = structs.DatacenterConfigUpsert
	resp, err := c.srv.raftApply(structs.DatacenterConfigRequestType, args)
	if err != nil {
		return err
	}
	if respErr, ok := resp.(error); ok {
		return respErr
	}
	if respBool, ok := resp.(bool); ok {
		*reply = respBool
	}

	return nil
}

func (c *DatacenterConfig) Delete(args *structs.DatacenterConfigRequest, reply *bool) error {
	// Ensure that all datacenter config writes go to the primary datacenter. These will then
	// be replicated to all the other datacenters.
	args.Datacenter = c.srv.config.PrimaryDatacenter

	if done, err := c.srv.forward("DatacenterConfig.Delete", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"datacenter_config", "delete"}, time.Now())

	// Fetch the ACL token, if any.
	rule, err := c.srv.ResolveToken(args.Token)
	if err != nil {
		return err
	}
	// TODO (namespaces) use actual ent authz context
	if rule != nil && rule.OperatorWrite(nil) != acl.Allow {
		return acl.ErrPermissionDenied
	}

	args.Op = structs.DatacenterConfigDelete
	resp, err := c.srv.raftApply(structs.DatacenterConfigRequestType, args)
	if err != nil {
		return err
	}
	if respErr, ok := resp.(error); ok {
		return respErr
	}
	if respBool, ok := resp.(bool); ok {
		*reply = respBool
	}

	return nil
}

func (c *DatacenterConfig) Get(args *structs.DatacenterConfigQuery, reply *structs.DatacenterConfigResponse) error {
	if done, err := c.srv.forward("DatacenterConfig.Get", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"datacenter_config", "get"}, time.Now())

	// Fetch the ACL token, if any.
	rule, err := c.srv.ResolveToken(args.Token)
	if err != nil {
		return err
	}
	// TODO (namespaces) use actual ent authz context
	if rule != nil && rule.OperatorRead(nil) != acl.Allow {
		return acl.ErrPermissionDenied
	}

	return c.srv.blockingQuery(
		&args.QueryOptions,
		&reply.QueryMeta,
		func(ws memdb.WatchSet, state *state.Store) error {
			index, config, err := state.DatacenterConfigGet(ws, args.Datacenter)
			if err != nil {
				return err
			}

			reply.Index = index
			if config == nil {
				return nil
			}

			reply.Config = config
			return nil
		})
}

func (c *DatacenterConfig) List(args *structs.DCSpecificRequest, reply *structs.IndexedDatacenterConfigs) error {
	if done, err := c.srv.forward("DatacenterConfig.List", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"datacenter_config", "list"}, time.Now())

	// Fetch the ACL token, if any.
	rule, err := c.srv.ResolveToken(args.Token)
	if err != nil {
		return err
	}
	// TODO (namespaces) use actual ent authz context
	if rule != nil && rule.OperatorRead(nil) != acl.Allow {
		return acl.ErrPermissionDenied
	}

	return c.srv.blockingQuery(
		&args.QueryOptions,
		&reply.QueryMeta,
		func(ws memdb.WatchSet, state *state.Store) error {
			index, configs, err := state.DatacenterConfigList(ws)
			if err != nil {
				return err
			}

			reply.Index = index
			if len(configs) == 0 {
				reply.Configs = []*structs.DatacenterConfig{}
				return nil
			}

			reply.Configs = configs
			return nil
		})
}
