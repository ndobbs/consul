package state

import (
	"fmt"

	"github.com/hashicorp/consul/agent/structs"
	memdb "github.com/hashicorp/go-memdb"
)

const datacenterConfigTableName = "datacenter-configs"

func datacenterConfigTableSchema() *memdb.TableSchema {
	return &memdb.TableSchema{
		Name: datacenterConfigTableName,
		Indexes: map[string]*memdb.IndexSchema{
			"id": &memdb.IndexSchema{
				Name:         "id",
				AllowMissing: false,
				Unique:       true,
				Indexer: &memdb.StringFieldIndex{
					Field:     "Datacenter",
					Lowercase: true,
				},
			},
		},
	}
}

func init() {
	registerSchema(datacenterConfigTableSchema)
}

// DatacenterConfigs is used to pull all the datacenter configs for the snapshot.
func (s *Snapshot) DatacenterConfigs() ([]*structs.DatacenterConfig, error) {
	configs, err := s.tx.Get(datacenterConfigTableName, "id")
	if err != nil {
		return nil, err
	}

	var ret []*structs.DatacenterConfig
	for wrapped := configs.Next(); wrapped != nil; wrapped = configs.Next() {
		ret = append(ret, wrapped.(*structs.DatacenterConfig))
	}

	return ret, nil
}

// DatacenterConfig is used when restoring from a snapshot.
func (s *Restore) DatacenterConfig(g *structs.DatacenterConfig) error {
	// Insert
	if err := s.tx.Insert(datacenterConfigTableName, g); err != nil {
		return fmt.Errorf("failed restoring datacenter config object: %s", err)
	}
	if err := indexUpdateMaxTxn(s.tx, g.ModifyIndex, datacenterConfigTableName); err != nil {
		return fmt.Errorf("failed updating index: %s", err)
	}

	return nil
}

func (s *Store) DatacenterConfigBatchSet(idx uint64, configs structs.DatacenterConfigs) error {
	tx := s.db.Txn(true)
	defer tx.Abort()

	for _, config := range configs {
		if err := s.datacenterConfigSetTxn(tx, idx, config); err != nil {
			return err
		}
	}

	tx.Commit()
	return nil
}

// DatacenterConfigSet is called to do an upsert of a given datacenter config.
func (s *Store) DatacenterConfigSet(idx uint64, config *structs.DatacenterConfig) error {
	tx := s.db.Txn(true)
	defer tx.Abort()

	if err := s.datacenterConfigSetTxn(tx, idx, config); err != nil {
		return err
	}

	tx.Commit()
	return nil
}

// datacenterConfigSetTxn upserts a datacenter config inside of a transaction.
func (s *Store) datacenterConfigSetTxn(tx *memdb.Txn, idx uint64, config *structs.DatacenterConfig) error {
	if config.Datacenter == "" {
		return fmt.Errorf("missing datacenter on datacenter config")
	}

	// Check for existing.
	var existing *structs.DatacenterConfig
	existingRaw, err := tx.First(datacenterConfigTableName, "id", config.Datacenter)
	if err != nil {
		return fmt.Errorf("failed datacenter config lookup: %s", err)
	}

	if existingRaw != nil {
		existing = existingRaw.(*structs.DatacenterConfig)
	}

	// Set the indexes
	if existing != nil {
		config.CreateIndex = existing.CreateIndex
		config.ModifyIndex = idx
	} else {
		config.CreateIndex = idx
		config.ModifyIndex = idx
	}

	// Insert the datacenter config and update the index
	if err := tx.Insert(datacenterConfigTableName, config); err != nil {
		return fmt.Errorf("failed inserting datacenter config: %s", err)
	}
	if err := tx.Insert("index", &IndexEntry{datacenterConfigTableName, idx}); err != nil {
		return fmt.Errorf("failed updating index: %v", err)
	}

	return nil
}

// DatacenterConfigGet is called to get a datacenter config.
func (s *Store) DatacenterConfigGet(ws memdb.WatchSet, datacenter string) (uint64, *structs.DatacenterConfig, error) {
	tx := s.db.Txn(false)
	defer tx.Abort()
	return s.datacenterConfigGetTxn(tx, ws, datacenter)
}

func (s *Store) datacenterConfigGetTxn(tx *memdb.Txn, ws memdb.WatchSet, datacenter string) (uint64, *structs.DatacenterConfig, error) {
	// Get the index
	idx := maxIndexTxn(tx, datacenterConfigTableName)

	// Get the existing contents.
	watchCh, existing, err := tx.FirstWatch(datacenterConfigTableName, "id", datacenter)
	if err != nil {
		return 0, nil, fmt.Errorf("failed datacenter config lookup: %s", err)
	}
	ws.Add(watchCh)

	if existing == nil {
		return idx, nil, nil
	}

	config, ok := existing.(*structs.DatacenterConfig)
	if !ok {
		return 0, nil, fmt.Errorf("datacenter config %q is an invalid type: %T", datacenter, config)
	}

	return idx, config, nil
}

// DatacenterConfigList is called to get all datacenter config objects.
func (s *Store) DatacenterConfigList(ws memdb.WatchSet) (uint64, []*structs.DatacenterConfig, error) {
	tx := s.db.Txn(false)
	defer tx.Abort()
	return s.datacenterConfigListTxn(tx, ws)
}

func (s *Store) datacenterConfigListTxn(tx *memdb.Txn, ws memdb.WatchSet) (uint64, []*structs.DatacenterConfig, error) {
	// Get the index
	idx := maxIndexTxn(tx, datacenterConfigTableName)

	iter, err := tx.Get(datacenterConfigTableName, "id")
	if err != nil {
		return 0, nil, fmt.Errorf("failed datacenter config lookup: %s", err)
	}
	ws.Add(iter.WatchCh())

	var results []*structs.DatacenterConfig
	for v := iter.Next(); v != nil; v = iter.Next() {
		results = append(results, v.(*structs.DatacenterConfig))
	}
	return idx, results, nil
}

func (s *Store) DatacenterConfigDelete(idx uint64, datacenter string) error {
	tx := s.db.Txn(true)
	defer tx.Abort()

	if err := s.datacenterConfigDeleteTxn(tx, idx, datacenter); err != nil {
		return err
	}

	tx.Commit()
	return nil
}

func (s *Store) DatacenterConfigBatchDelete(idx uint64, datacenters []string) error {
	tx := s.db.Txn(true)
	defer tx.Abort()

	for _, datacenter := range datacenters {
		if err := s.datacenterConfigDeleteTxn(tx, idx, datacenter); err != nil {
			return err
		}
	}

	tx.Commit()
	return nil
}

func (s *Store) datacenterConfigDeleteTxn(tx *memdb.Txn, idx uint64, datacenter string) error {
	// Try to retrieve the existing datacenter config.
	existing, err := tx.First(datacenterConfigTableName, "id", datacenter)
	if err != nil {
		return fmt.Errorf("failed datacenter config lookup: %s", err)
	}
	if existing == nil {
		return nil
	}

	// Delete the datacenter config from the DB and update the index.
	if err := tx.Delete(datacenterConfigTableName, existing); err != nil {
		return fmt.Errorf("failed removing datacenter config: %s", err)
	}
	if err := tx.Insert("index", &IndexEntry{datacenterConfigTableName, idx}); err != nil {
		return fmt.Errorf("failed updating index: %s", err)
	}
	return nil
}
