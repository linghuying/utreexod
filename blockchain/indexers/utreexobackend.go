// Copyright (c) 2021 The utreexo developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package indexers

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/utreexo/utreexo"
	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
)

const (
	// utreexoDirName is the name of the directory in which the utreexo state
	// is stored.
	utreexoDirName         = "utreexostate"
	defaultUtreexoFileName = "forest.dat"
)

var (
	// utreexoStateConsistencyKeyName is name of the db key used to store the consistency
	// state for the utreexo accumulator state.
	utreexoStateConsistencyKeyName = []byte("utreexostateconsistency")
)

// UtreexoConfig is a descriptor which specifies the Utreexo state instance configuration.
type UtreexoConfig struct {
	// MaxMemoryUsage is the desired memory usage for the utreexo state cache.
	MaxMemoryUsage int64

	// Params are the Bitcoin network parameters. This is used to separately store
	// different accumulators.
	Params *chaincfg.Params

	// If the node is a pruned node or not.
	Pruned bool

	// DataDir is the base path of where all the data for this node will be stored.
	// Utreexo has custom storage method and that data will be stored under this
	// directory.
	DataDir string

	// Name is what the type of utreexo proof indexer this utreexo state is related
	// to.
	Name string
}

// UtreexoState is a wrapper around the raw accumulator with configuration
// information.  It contains the entire, non-pruned accumulator.
type UtreexoState struct {
	config         *UtreexoConfig
	state          utreexo.Utreexo
	utreexoStateDB *leveldb.DB

	isFlushNeeded func() bool
	flush         func(ldbTx *leveldb.Transaction) error
}

// utreexoBasePath returns the base path of where the utreexo state should be
// saved to with the with UtreexoConfig information.
func utreexoBasePath(cfg *UtreexoConfig) string {
	return filepath.Join(cfg.DataDir, utreexoDirName+"_"+cfg.Name)
}

// deleteUtreexoState removes the utreexo state directory and all the contents
// in it.
func deleteUtreexoState(path string) error {
	_, err := os.Stat(path)
	if err == nil {
		log.Infof("Deleting the utreexo state at directory %s", path)
	} else {
		log.Infof("No utreexo state to delete")
	}
	return os.RemoveAll(path)
}

// checkUtreexoExists checks that the data for this utreexo state type specified
// in the config is present and should be resumed off of.
func checkUtreexoExists(cfg *UtreexoConfig, basePath string) bool {
	path := filepath.Join(basePath, defaultUtreexoFileName)
	_, err := os.Stat(path)
	if err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}

// dbWriteUtreexoStateConsistency writes the consistency state to the database using the given transaction.
func dbWriteUtreexoStateConsistency(ldbTx *leveldb.Transaction, bestHash *chainhash.Hash, numLeaves uint64) error {
	// Create the byte slice to be written.
	var buf [8 + chainhash.HashSize]byte
	binary.LittleEndian.PutUint64(buf[:8], numLeaves)
	copy(buf[8:], bestHash[:])

	return ldbTx.Put(utreexoStateConsistencyKeyName, buf[:], nil)
}

// dbFetchUtreexoStateConsistency returns the stored besthash and the numleaves in the database.
func dbFetchUtreexoStateConsistency(db *leveldb.DB) (*chainhash.Hash, uint64, error) {
	buf, err := db.Get(utreexoStateConsistencyKeyName, nil)
	if err != nil && err != leveldb.ErrNotFound {
		return nil, 0, err
	}
	// Set error to nil as the error may have been ErrNotFound.
	err = nil
	if buf == nil {
		return nil, 0, nil
	}

	bestHash, err := chainhash.NewHash(buf[8:])
	if err != nil {
		return nil, 0, err
	}

	return bestHash, binary.LittleEndian.Uint64(buf[:8]), nil
}

// FetchCurrentUtreexoState returns the current utreexo state.
func (idx *UtreexoProofIndex) FetchCurrentUtreexoState() ([]*chainhash.Hash, uint64) {
	idx.mtx.RLock()
	defer idx.mtx.RUnlock()

	roots := idx.utreexoState.state.GetRoots()
	chainhashRoots := make([]*chainhash.Hash, len(roots))

	for i, root := range roots {
		newRoot := chainhash.Hash(root)
		chainhashRoots[i] = &newRoot
	}

	return chainhashRoots, idx.utreexoState.state.GetNumLeaves()
}

// FetchCurrentUtreexoState returns the current utreexo state.
func (idx *FlatUtreexoProofIndex) FetchCurrentUtreexoState() ([]*chainhash.Hash, uint64) {
	idx.mtx.RLock()
	defer idx.mtx.RUnlock()

	roots := idx.utreexoState.state.GetRoots()

	chainhashRoots := make([]*chainhash.Hash, len(roots))
	for i, root := range roots {
		newRoot := chainhash.Hash(root)
		chainhashRoots[i] = &newRoot
	}

	return chainhashRoots, idx.utreexoState.state.GetNumLeaves()
}

// FetchUtreexoState returns the utreexo state at the desired block.
func (idx *UtreexoProofIndex) FetchUtreexoState(dbTx database.Tx, blockHash *chainhash.Hash) ([]*chainhash.Hash, uint64, error) {
	stump, err := dbFetchUtreexoState(dbTx, blockHash)
	if err != nil {
		return nil, 0, err
	}

	chainhashRoots := make([]*chainhash.Hash, len(stump.Roots))
	for i, root := range stump.Roots {
		newRoot := chainhash.Hash(root)
		chainhashRoots[i] = &newRoot
	}
	return chainhashRoots, stump.NumLeaves, nil
}

// FetchUtreexoState returns the utreexo state at the desired block.
func (idx *FlatUtreexoProofIndex) FetchUtreexoState(blockHeight int32) ([]*chainhash.Hash, uint64, error) {
	stump, err := idx.fetchRoots(blockHeight)
	if err != nil {
		return nil, 0, err
	}

	chainhashRoots := make([]*chainhash.Hash, len(stump.Roots))
	for i, root := range stump.Roots {
		newRoot := chainhash.Hash(root)
		chainhashRoots[i] = &newRoot
	}
	return chainhashRoots, stump.NumLeaves, nil
}

// FlushUtreexoStateIfNeeded flushes the utreexo state only if the cache is full.
func (idx *UtreexoProofIndex) FlushUtreexoStateIfNeeded(bestHash *chainhash.Hash) error {
	if idx.utreexoState.isFlushNeeded() {
		return idx.FlushUtreexoState(bestHash)
	}
	return nil
}

// FlushUtreexoState saves the utreexo state to disk.
func (idx *UtreexoProofIndex) FlushUtreexoState(bestHash *chainhash.Hash) error {
	idx.mtx.Lock()
	defer idx.mtx.Unlock()

	log.Infof("Flushing the utreexo state to disk...")

	ldbTx, err := idx.utreexoState.utreexoStateDB.OpenTransaction()
	if err != nil {
		return err
	}

	// Write the best block hash and the numleaves for the utreexo state.
	err = dbWriteUtreexoStateConsistency(ldbTx, bestHash, idx.utreexoState.state.GetNumLeaves())
	if err != nil {
		return err
	}

	err = idx.utreexoState.flush(ldbTx)
	if err != nil {
		ldbTx.Discard()
		return err
	}

	err = ldbTx.Commit()
	if err != nil {
		ldbTx.Discard()
		return err
	}

	log.Infof("Finished flushing the utreexo state to disk.")

	return nil
}

// CloseUtreexoState flushes and closes the utreexo database state.
func (idx *UtreexoProofIndex) CloseUtreexoState(bestHash *chainhash.Hash) error {
	err := idx.FlushUtreexoState(bestHash)
	if err != nil {
		log.Warnf("error whiling flushing the utreexo state. %v", err)
	}
	return idx.utreexoState.utreexoStateDB.Close()
}

// FlushUtreexoStateIfNeeded flushes the utreexo state only if the cache is full.
func (idx *FlatUtreexoProofIndex) FlushUtreexoStateIfNeeded(bestHash *chainhash.Hash) error {
	if idx.utreexoState.isFlushNeeded() {
		return idx.FlushUtreexoState(bestHash)
	}
	return nil
}

// FlushUtreexoState saves the utreexo state to disk.
func (idx *FlatUtreexoProofIndex) FlushUtreexoState(bestHash *chainhash.Hash) error {
	idx.mtx.Lock()
	defer idx.mtx.Unlock()

	ldbTx, err := idx.utreexoState.utreexoStateDB.OpenTransaction()
	if err != nil {
		return err
	}

	// Write the best block hash and the numleaves for the utreexo state.
	err = dbWriteUtreexoStateConsistency(ldbTx, bestHash, idx.utreexoState.state.GetNumLeaves())
	if err != nil {
		return err
	}

	err = idx.utreexoState.flush(ldbTx)
	if err != nil {
		ldbTx.Discard()
		return err
	}

	err = ldbTx.Commit()
	if err != nil {
		ldbTx.Discard()
		return err
	}

	return nil
}

// CloseUtreexoState flushes and closes the utreexo database state.
func (idx *FlatUtreexoProofIndex) CloseUtreexoState(bestHash *chainhash.Hash) error {
	err := idx.FlushUtreexoState(bestHash)
	if err != nil {
		log.Warnf("error whiling flushing the utreexo state. %v", err)
	}
	return idx.utreexoState.utreexoStateDB.Close()
}

// serializeUndoBlock serializes all the data that's needed for undoing a full utreexo state
// into a slice of bytes.
func serializeUndoBlock(numAdds uint64, targets []uint64, delHashes []utreexo.Hash) ([]byte, error) {
	numAddsSize := 8
	targetCountSize := 4
	targetsSize := len(targets) * 8
	delHashesCountSize := 4
	delHashesSize := len(delHashes) * chainhash.HashSize

	w := bytes.NewBuffer(make([]byte, 0, numAddsSize+targetCountSize+targetsSize+delHashesCountSize+delHashesSize))

	// Write numAdds.
	buf := make([]byte, numAddsSize)
	byteOrder.PutUint64(buf[:], numAdds)
	_, err := w.Write(buf[:])
	if err != nil {
		return nil, err
	}

	// Write the targets.
	//
	// Targets are prefixed with the count in uint32.
	buf = buf[:targetCountSize]
	byteOrder.PutUint32(buf[:], uint32(len(targets)))
	_, err = w.Write(buf[:])
	if err != nil {
		return nil, err
	}
	buf = buf[:8]
	for _, targ := range targets {
		byteOrder.PutUint64(buf[:], targ)

		_, err = w.Write(buf[:])
		if err != nil {
			return nil, err
		}
	}

	// Write the delHashes.
	//
	// DelHashes are prefixed with the count in uint32.
	buf = buf[:delHashesCountSize]
	byteOrder.PutUint32(buf[:], uint32(len(delHashes)))
	_, err = w.Write(buf[:])
	if err != nil {
		return nil, err
	}
	for _, hash := range delHashes {
		_, err = w.Write(hash[:])
		if err != nil {
			return nil, err
		}
	}

	return w.Bytes(), nil
}

// deserializeUndoBlock deserializes all the data that's needed to undo a full utreexo
// state from a slice of serialized bytes.
func deserializeUndoBlock(serialized []byte) (uint64, []uint64, []utreexo.Hash, error) {
	r := bytes.NewReader(serialized)

	// Read the numAdds.
	buf := make([]byte, chainhash.HashSize)
	buf = buf[:8]
	_, err := r.Read(buf)
	if err != nil {
		return 0, nil, nil, err
	}

	numAdds := byteOrder.Uint64(buf)

	// Read the targets.
	buf = buf[:4]
	_, err = r.Read(buf)
	if err != nil {
		return 0, nil, nil, err
	}

	targLen := byteOrder.Uint32(buf)
	targets := make([]uint64, targLen)

	buf = buf[:8]
	for i := range targets {
		_, err = r.Read(buf)
		if err != nil {
			return 0, nil, nil, err
		}

		targets[i] = byteOrder.Uint64(buf)
	}

	// Read the delHashes.
	buf = buf[:4]
	_, err = r.Read(buf)
	if err != nil {
		return 0, nil, nil, err
	}
	hashLen := byteOrder.Uint32(buf)
	delHashes := make([]utreexo.Hash, hashLen)

	buf = buf[:chainhash.HashSize]
	for i := range delHashes {
		_, err = r.Read(buf)
		if err != nil {
			return 0, nil, nil, err
		}

		delHashes[i] = *(*utreexo.Hash)(buf)
	}

	return numAdds, targets, delHashes, nil
}

// InitUtreexoState returns an initialized utreexo state. If there isn't an
// existing state on disk, it creates one and returns it.
// maxMemoryUsage of 0 will keep every element on disk. A negaive maxMemoryUsage will
// load every element to the memory.
func InitUtreexoState(cfg *UtreexoConfig) (*UtreexoState, error) {
	log.Infof("Initializing Utreexo state from '%s'", utreexoBasePath(cfg))
	defer log.Info("Utreexo state loaded")

	p := utreexo.NewMapPollard(true)

	maxNodesMem := cfg.MaxMemoryUsage * 7 / 10
	maxCachedLeavesMem := cfg.MaxMemoryUsage - maxNodesMem

	db, err := leveldb.OpenFile(utreexoBasePath(cfg), nil)
	if err != nil {
		return nil, err
	}

	nodesDB, err := blockchain.InitNodesBackEnd(db, maxNodesMem)
	if err != nil {
		return nil, err
	}

	cachedLeavesDB, err := blockchain.InitCachedLeavesBackEnd(db, maxCachedLeavesMem)
	if err != nil {
		return nil, err
	}

	_, numLeaves, err := dbFetchUtreexoStateConsistency(db)
	if err != nil {
		return nil, err
	}
	p.NumLeaves = numLeaves

	var flush func(ldbTx *leveldb.Transaction) error
	var isFlushNeeded func() bool
	if cfg.MaxMemoryUsage >= 0 {
		p.Nodes = nodesDB
		p.CachedLeaves = cachedLeavesDB
		flush = func(ldbTx *leveldb.Transaction) error {
			nodesUsed, nodesCapacity := nodesDB.UsageStats()
			log.Debugf("Utreexo index nodesDB cache usage: %d/%d (%v%%)\n",
				nodesUsed, nodesCapacity,
				float64(nodesUsed)/float64(nodesCapacity))

			cachedLeavesUsed, cachedLeavesCapacity := cachedLeavesDB.UsageStats()
			log.Debugf("Utreexo index cachedLeavesDB cache usage: %d/%d (%v%%)\n",
				cachedLeavesUsed, cachedLeavesCapacity,
				float64(cachedLeavesUsed)/float64(cachedLeavesCapacity))

			err = nodesDB.Flush(ldbTx)
			if err != nil {
				return err
			}
			err = cachedLeavesDB.Flush(ldbTx)
			if err != nil {
				return err
			}

			return nil
		}
		isFlushNeeded = func() bool {
			nodesNeedsFlush := nodesDB.IsFlushNeeded()
			leavesNeedsFlush := cachedLeavesDB.IsFlushNeeded()
			return nodesNeedsFlush && leavesNeedsFlush
		}
	} else {
		log.Infof("loading the utreexo state from disk...")
		err = nodesDB.ForEach(func(k uint64, v utreexo.Leaf) error {
			p.Nodes.Put(k, v)
			return nil
		})
		if err != nil {
			return nil, err
		}

		err = cachedLeavesDB.ForEach(func(k utreexo.Hash, v uint64) error {
			p.CachedLeaves.Put(k, v)
			return nil
		})
		if err != nil {
			return nil, err
		}

		log.Infof("Finished loading the utreexo state from disk.")

		flush = func(ldbTx *leveldb.Transaction) error {
			err = p.Nodes.ForEach(func(k uint64, v utreexo.Leaf) error {
				return blockchain.NodesBackendPut(ldbTx, k, v)
			})
			if err != nil {
				return err
			}

			err = p.CachedLeaves.ForEach(func(k utreexo.Hash, v uint64) error {
				return blockchain.CachedLeavesBackendPut(ldbTx, k, v)
			})
			if err != nil {
				return err
			}

			return nil
		}

		// Flush is never needed since we're keeping everything in memory.
		isFlushNeeded = func() bool {
			return false
		}
	}

	uState := &UtreexoState{
		config:         cfg,
		state:          &p,
		utreexoStateDB: db,
		isFlushNeeded:  isFlushNeeded,
		flush:          flush,
	}

	return uState, err
}
