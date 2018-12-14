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

	metric metric // time to produce the block
}

type metric struct {
	ts time.Time // start
	tp time.Time // consensus Prepare
	tmc time.Time // make current
	tat time.Time  // apply transactions
	tf time.Time // consensus Finalize
	tsl time.Time // sonsensus Seal
	twbs time.Time // chain's Write BlockWithState
}

// task contains all information for consensus engine sealing and result submitting.
type task struct {
	receipts  []*types.Receipt
	state     *state.StateDB
	block     *types.Block
	//createdAt time.Time
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

	// atomic status counters
	running int32 // The indicator whether the consensus engine is running or not.

	// only used in the api of miner
	snapshotMu    sync.RWMutex // The lock used to protect the block snapshot and state snapshot
	snapshotBlock *types.Block
	snapshotState *state.StateDB
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
	defer ticker.Stop()

	for {
		select {
		//case <- w.startCH: //TODO, use this to start the mining process
		case now := <-ticker.C:
			timestamp = now.Unix()
			if w.isRunning() {
				if timestamp % blockInterval == 0 {  // check it is time to mint block
					w.mintBlock(timestamp) // TODO: go routine, stopChan in the future
				}
			}
			// for the next block
		case <-w.exitCh:
			return
		}
	}
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