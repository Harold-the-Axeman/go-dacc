// Copyright 2015 The go-dacc Authors
// This file is part of the go-dacc library.
//
// The go-dacc library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-dacc library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-dacc library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/daccproject/go-dacc/common"
	"github.com/daccproject/go-dacc/consensus"
	"github.com/daccproject/go-dacc/core"
	"github.com/daccproject/go-dacc/core/state"
	"github.com/daccproject/go-dacc/core/types"
	"github.com/daccproject/go-dacc/event"
	"github.com/daccproject/go-dacc/log"
	"github.com/daccproject/go-dacc/params"
)

const (
	blockInterval = 5 //TODO: config from dpos
)


// environment is the worker's current environment and holds all of the current state information.
type environment struct {
	signer types.Signer

	state *state.StateDB // apply state changes here
	dposContext *types.DposContext

	tcount  int           // tx count in cycle
	gasPool *core.GasPool // available gas used to pack transactions

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt

	//TODO
	timestmap int64 // time to produce the block

}

// task contains all information for consensus engine sealing and result submitting.
type task struct {
	receipts  []*types.Receipt
	state     *state.StateDB
	block     *types.Block
	createdAt time.Time
}

// worker is the main object which takes care of submitting new work to consensus engine
// and gathering the sealing result.
type worker struct {
	config *params.ChainConfig
	engine consensus.Engine
	eth    Backend
	chain  *core.BlockChain

	gasFloor uint64
	gasCeil  uint64

	// Subscriptions
	mux          *event.TypeMux
	exitCh    chan struct{}

	current *environment // An environment for current running cycle.

	mu       sync.RWMutex // The lock used to protect the coinbase and extra fields
	coinbase common.Address
	extra    []byte

	snapshotMu    sync.RWMutex // The lock used to protect the block snapshot and state snapshot
	snapshotBlock *types.Block
	snapshotState *state.StateDB

	// atomic status counters
	running int32 // The indicator whether the consensus engine is running or not.
}

func newWorker(config *params.ChainConfig, engine consensus.Engine, eth Backend, mux *event.TypeMux, recommit time.Duration, gasFloor, gasCeil uint64) *worker {
	worker := &worker{
		config:       config,
		engine:       engine,
		eth:          eth,
		chain:        eth.BlockChain(),
		gasFloor:     gasFloor,
		gasCeil:      gasCeil,
		mux:          mux,
		exitCh:       make(chan struct{}),
	}

	go worker.mainLoop()

	return worker
}

// newWorkLoop is a standalone goroutine to submit new mining work upon received events.
func (w *worker) mainLoop() {
	var timestamp int64
	ticker := time.NewTicker(time.Second)

	for {
		select {
		//case <- w.startCH: //TODO, use this to start the mining process
		case now := <-ticker.C:
			timestamp = now.Unix()
			if w.isRunning() {
				if timestamp % blockInterval == 0 {  // check it is time to mint block
					w.mintBlock(timestamp)
				}
			}
			// for the next block
		case <-w.exitCh:
			return
		}
	}
}

// taskLoop is a standalone goroutine to fetch sealing task from the generator and
func (w *worker) commitTask(task *task) {
	var (
		stopCh chan struct{}
		prev   common.Hash
	)

	// interrupt aborts the in-flight sealing task.
	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}

	/*if w.newTaskHook != nil {
		w.newTaskHook(task)
	}*/
	// Reject duplicate sealing work due to resubmitting.
	sealHash := w.engine.SealHash(task.block.Header())
	if sealHash == prev {
		log.Info("Miner: commitTask. This should not happen")
		return
	}
	// Interrupt previous sealing operation
	interrupt()
	stopCh, prev = make(chan struct{}), sealHash

/*	if w.skipSealHook != nil && w.skipSealHook(task) {
		return
	}*/
	//TODO:
	block, err := w.engine.Seal(w.chain, task.block, stopCh)
	if err != nil {
		log.Warn("Block sealing failed", "err", err)
	}
	w.processResult(block, task)
}

// resultLoop is a standalone goroutine to handle sealing result submitting
// processResult the result of the
func (w *worker) processResult(block *types.Block, task *task) {
	if block == nil {
		return
	}
	// Short circuit when receiving duplicate result caused by resubmitting.
	if w.chain.HasBlock(block.Hash(), block.NumberU64()) {
		return
	}
	var (
		sealhash = w.engine.SealHash(block.Header())
		hash     = block.Hash()
	)

	// Different block could share same sealhash, deep copy here to prevent write-write conflict.
	var (
		receipts = make([]*types.Receipt, len(task.receipts))
		logs     []*types.Log
	)
	for i, receipt := range task.receipts {
		receipts[i] = new(types.Receipt)
		*receipts[i] = *receipt
		// Update the block hash in all logs since it is now available and not when the
		// receipt/log of individual transactions were created.
		for _, log := range receipt.Logs {
			log.BlockHash = hash
		}
		logs = append(logs, receipt.Logs...)
	}
	// Commit block and state to database.
	var beginWriteBlock = time.Now()
	log.Warn("Begin WriteBlockWithState")
	stat, err := w.chain.WriteBlockWithState(block, receipts, task.state)
	log.Warn("End WriteBlockWithState", "Cost", time.Since(beginWriteBlock))
	if err != nil {
		log.Error("Failed writing block to chain", "err", err)
		return
	}
	log.Info("😄 Successfully sealed new block", "number", block.Number(), "sealhash", sealhash, "hash", hash,
		"elapsed", common.PrettyDuration(time.Since(task.createdAt)))

	// Broadcast the block and announce chain insertion event
	w.mux.Post(core.NewMinedBlockEvent{Block: block})

	var events []interface{}
	switch stat {
	case core.CanonStatTy:
		events = append(events, core.ChainEvent{Block: block, Hash: block.Hash(), Logs: logs})
		events = append(events, core.ChainHeadEvent{Block: block})
	case core.SideStatTy:
		// Change by Shara ,
		//events = append(events, core.ChainSideEvent{Block: block})
		// End Change by Shara
	}
	w.chain.PostChainEvents(events, logs)
}

// start sets the running status as 1 and triggers new work submitting.
func (w *worker) start() {
	atomic.StoreInt32(&w.running, 1)
	//w.startCh <- struct{}{}
}

// stop sets the running status as 0.
func (w *worker) stop() {
	atomic.StoreInt32(&w.running, 0)
}

// isRunning returns an indicator whether worker is running or not.
func (w *worker) isRunning() bool {
	return atomic.LoadInt32(&w.running) == 1
}

// close terminates all background threads maintained by the worker.
// Note the worker does not support being closed multiple times.
func (w *worker) close() {
	close(w.exitCh)
}

// setEtherbase sets the etherbase used to initialize the block coinbase field.
func (w *worker) setCoinbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

// setExtra sets the content used to initialize the block extra field.
func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extra = extra
}

// pending returns the pending state and corresponding block.
func (w *worker) pending() (*types.Block, *state.StateDB) {
	// return a snapshot to avoid contention on currentMu mutex
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	if w.snapshotState == nil {
		return nil, nil
	}
	return w.snapshotBlock, w.snapshotState.Copy()
}

// pendingBlock returns pending block.
func (w *worker) pendingBlock() *types.Block {
	// return a snapshot to avoid contention on currentMu mutex
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock
}