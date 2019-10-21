package agent

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/consul/agent/structs"
)

func (s *HTTPServer) DatacenterConfigCreate(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	return s.datacenterConfigSet(resp, req, "")
}

// DatacenterConfigCRUD switches on the different CRUD operations for datacenter configs.
func (s *HTTPServer) DatacenterConfigCRUD(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	var fn func(resp http.ResponseWriter, req *http.Request, datacenterName string) (interface{}, error)
	switch req.Method {
	case "GET":
		fn = s.datacenterConfigGet

	case "PUT":
		fn = s.datacenterConfigSet

	case "DELETE":
		fn = s.datacenterConfigDelete

	default:
		return nil, MethodNotAllowedError{req.Method, []string{"GET", "PUT", "DELETE"}}
	}

	datacenterName := strings.TrimPrefix(req.URL.Path, "/v1/datacenter-config/")
	if datacenterName == "" && req.Method != "PUT" {
		return nil, BadRequestError{Reason: "Missing datacenter name"}
	}

	return fn(resp, req, datacenterName)
}

func (s *HTTPServer) datacenterConfigGet(resp http.ResponseWriter, req *http.Request, datacenter string) (interface{}, error) {
	args := structs.DatacenterConfigQuery{
		Datacenter: datacenter,
	}
	if done := s.parse(resp, req, &args.TargetDatacenter, &args.QueryOptions); done {
		return nil, nil
	}

	var out structs.DatacenterConfigResponse
	defer setMeta(resp, &out.QueryMeta)
	if err := s.agent.RPC("DatacenterConfig.Get", &args, &out); err != nil {
		return nil, err
	}

	if out.Config == nil {
		resp.WriteHeader(http.StatusNotFound)
		return nil, nil
	}

	return out, nil
}

func (s *HTTPServer) datacenterConfigDelete(resp http.ResponseWriter, req *http.Request, datacenter string) (interface{}, error) {
	args := structs.DatacenterConfigRequest{
		Datacenter: s.agent.config.Datacenter,
		Op:         structs.DatacenterConfigDelete,
		Config:     &structs.DatacenterConfig{},
	}
	s.parseDC(req, &args.Config.Datacenter)
	s.parseToken(req, &args.Token)
	// TODO(namespaces): does this need an ent meta?

	var ignored bool
	if err := s.agent.RPC("DatacenterConfig.Delete", args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}

func (s *HTTPServer) DatacenterConfigList(resp http.ResponseWriter, req *http.Request) (interface{}, error) {
	var args structs.DCSpecificRequest
	if done := s.parse(resp, req, &args.Datacenter, &args.QueryOptions); done {
		return nil, nil
	}

	if args.Datacenter == "" {
		args.Datacenter = s.agent.config.Datacenter
	}

	var out structs.IndexedDatacenterConfigs
	if err := s.agent.RPC("DatacenterConfig.List", &args, &out); err != nil {
		return nil, err
	}

	// make sure we return an array and not nil
	if out.Configs == nil {
		out.Configs = make(structs.DatacenterConfigs, 0)
	}

	return out.Configs, nil
}

func (s *HTTPServer) datacenterConfigSet(resp http.ResponseWriter, req *http.Request, datacenter string) (interface{}, error) {
	args := structs.DatacenterConfigRequest{
		Datacenter: s.agent.config.Datacenter,
		Op:         structs.DatacenterConfigUpsert,
		Config:     &structs.DatacenterConfig{},
	}
	s.parseToken(req, &args.Token)
	// TODO(namespaces): does this need an ent meta?

	if err := decodeBody(req.Body, &args.Config); err != nil {
		return nil, BadRequestError{Reason: fmt.Sprintf("Datacenter Config decoding failed: %v", err)}
	}

	if datacenter != "" {
		if args.Config.Datacenter != "" && args.Config.Datacenter != datacenter {
			return nil, BadRequestError{Reason: "Datacenter name in URL and payload do not match"}
		} else if args.Config.Datacenter == "" {
			args.Config.Datacenter = datacenter
		}
	}

	if args.Config.Datacenter == "" {
		return nil, BadRequestError{Reason: "Datacenter name not present in either URL or payload"}
	}

	var ignored bool
	if err := s.agent.RPC("DatacenterConfig.Apply", args, &ignored); err != nil {
		return nil, err
	}

	return true, nil
}
