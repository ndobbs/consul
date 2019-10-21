package structs

import (
	"sort"
	"time"
)

type DatacenterConfigOp string

const (
	DatacenterConfigUpsert DatacenterConfigOp = "upsert"
	DatacenterConfigDelete DatacenterConfigOp = "delete"
)

type DatacenterConfigRequest struct {
	Datacenter string
	Op         DatacenterConfigOp
	Config     *DatacenterConfig

	WriteRequest
}

func (c *DatacenterConfigRequest) RequestDatacenter() string {
	return c.Datacenter
}

type DatacenterConfigs []*DatacenterConfig

func (listings DatacenterConfigs) Sort() {
	sort.Slice(listings, func(i, j int) bool {
		return listings[i].Datacenter < listings[j].Datacenter
	})
}

type DatacenterConfig struct {
	Datacenter   string
	MeshGateways CheckServiceNodes `json:",omitempty"`
	UpdatedAt    time.Time
	RaftIndex
}

// TODO:
func (c *DatacenterConfig) IsSame(other *DatacenterConfig) bool {
	if c.Datacenter != other.Datacenter {
		return false
	}

	// We don't include the UpdatedAt field in this comparison because that is
	// only updated when we re-persist.

	if len(c.MeshGateways) != len(other.MeshGateways) {
		return false
	}

	// TODO: we don't bother to sort these since the order is going to be
	// already defined by how the catalog returns results which should be
	// stable enough.

	for i := 0; i < len(c.MeshGateways); i++ {
		a := c.MeshGateways[i]
		b := other.MeshGateways[i]

		if !a.Node.IsSame(b.Node) {
			return false
		}
		if !a.Service.IsSame(b.Service) {
			return false
		}

		if len(a.Checks) != len(b.Checks) {
			return false
		}

		for j := 0; j < len(a.Checks); j++ {
			ca := a.Checks[j]
			cb := b.Checks[j]

			if !ca.IsSame(cb) {
				return false
			}
		}
	}

	return true
}

type DatacenterConfigQuery struct {
	Datacenter string

	TargetDatacenter string
	QueryOptions
}

type DatacenterConfigResponse struct {
	Config *DatacenterConfig
	QueryMeta
}

type IndexedDatacenterConfigs struct {
	Configs DatacenterConfigs
	QueryMeta
}

func (c *DatacenterConfigQuery) RequestDatacenter() string {
	return c.TargetDatacenter
}

/*
before saving we should have the caller filter the []CheckServiceNode by:

func retainGateway(gateway structs.CheckServiceNode) bool {
	for _, chk := range gateway.Checks {
		if chk.Status == api.HealthCritical {
			return false
		}
	}
	return true
}

// TODO (mesh-gateway) - should we respect the translate_wan_addrs configuration here or just always use the wan for cross-dc?
addr, port := gateway.BestAddress(true)

*/
