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
	// resultQueueSize is the size of channel listening to sealing result.
	resultQueueSize = 10

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 4096

	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 10

	// staleThreshold is the maximum depth of the acceptable stale block.
	staleThreshold = 7
)

// environment is the worker's current environment and holds all of the current state information.
type environment struct {
	signer types.Signer

	state *state.StateDB // apply state changes here
	// Add by Shara
	dposContext *types.DposContext
	// end Add by Shara
	tcount  int           // tx count in cycle
	gasPool *core.GasPool // available gas used to pack transactions

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt
}

// task contains all information for consensus engine sealing and result submitting.
type task struct {
	receipts  []*types.Receipt
	state     *state.StateDB
	block     *types.Block
	createdAt time.Time
}

// newWorkReq represents a request for new sealing work submitting with relative interrupt notifier.
type newWorkReq struct {
	timestamp int64
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
	txsCh        chan core.NewTxsEvent
	txsSub       event.Subscription
	chainHeadCh  chan core.ChainHeadEvent
	chainHeadSub event.Subscription

	// Channels
	newWorkCh chan *newWorkReq
	taskCh    chan *task
	resultCh  chan *types.Block
	exitCh    chan struct{}

	current *environment // An environment for current running cycle.

	mu       sync.RWMutex // The lock used to protect the coinbase and extra fields
	coinbase common.Address
	extra    []byte

	pendingMu    sync.RWMutex
	pendingTasks map[common.Hash]*task

	snapshotMu    sync.RWMutex // The lock used to protect the block snapshot and state snapshot
	snapshotBlock *types.Block
	snapshotState *state.StateDB

	// atomic status counters
	running int32 // The indicator whether the consensus engine is running or not.
	newTxs  int32 // New arrival transaction count since last sealing work submitting.

	// Test hooks
	newTaskHook  func(*task)                        // Method to call upon receiving a new sealing task.
	skipSealHook func(*task) bool                   // Method to decide whether skipping the sealing.
	fullTaskHook func()                             // Method to call before pushing the full sealing task.
	resubmitHook func(time.Duration, time.Duration) // Method to call upon updating resubmitting interval.
}

func newWorker(config *params.ChainConfig, engine consensus.Engine, eth Backend, mux *event.TypeMux, recommit time.Duration, gasFloor, gasCeil uint64) *worker {
	worker := &worker{
		config:       config,
		engine:       engine,
		eth:          eth,
		mux:          mux,
		chain:        eth.BlockChain(),
		gasFloor:     gasFloor,
		gasCeil:      gasCeil,
		pendingTasks: make(map[common.Hash]*task),
		txsCh:        make(chan core.NewTxsEvent, txChanSize),
		chainHeadCh:  make(chan core.ChainHeadEvent, chainHeadChanSize),
		newWorkCh:    make(chan *newWorkReq),
		taskCh:       make(chan *task),
		resultCh:     make(chan *types.Block, resultQueueSize),
		exitCh:       make(chan struct{}),
	}
	// Subscribe NewTxsEvent for tx pool
	worker.txsSub = eth.TxPool().SubscribeNewTxsEvent(worker.txsCh)
	// Subscribe events for blockchain
	worker.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)
	// Change by Shara

	go worker.mainLoop()
	go worker.newWorkLoop()
	go worker.resultLoop()
	go worker.taskLoop()

	return worker
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

// newWorkLoop is a standalone goroutine to submit new mining work upon received events.
//func (w *worker) newWorkLoop(recommit time.Duration) {
func (w *worker) newWorkLoop() {
	var (
		timestamp int64 // timestamp for each round of mining.
	)

	commit := func() {
		w.newWorkCh <- &newWorkReq{timestamp: timestamp}
		atomic.StoreInt32(&w.newTxs, 0)
	}
	// clearPending cleans the stale pending tasks.
	clearPending := func(number uint64) {
		w.pendingMu.Lock()
		for h, t := range w.pendingTasks {
			if t.block.NumberU64()+staleThreshold <= number {
				delete(w.pendingTasks, h)
			}
		}
		w.pendingMu.Unlock()
	}

	// DPOS ticker for block producing, added by Harold
	tickerRep := time.NewTimer(time.Second)

	for {
		select {
		case <-w.chainHeadCh:
		// DPOS block producing ticker
		case now := <-tickerRep.C:
			//TODO: ticker will always run here, temporary fix here.
			if atomic.LoadInt32(&w.running) == 1 {
				clearPending(w.chain.CurrentBlock().NumberU64()) //NOTE
				timestamp = now.Unix()                           // TODO: NEED CHECK, possible bug: time.Now().Unix() or now, which one?
				commit()
			}
			// for the next block
			tickerRep.Reset(time.Second)

		case <-w.exitCh:
			return
		}
	}
}

// mainLoop is a standalone goroutine to regenerate the sealing task based on the received event.
func (w *worker) mainLoop() {
	defer w.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	for {
		select {
		case req := <-w.newWorkCh:
			w.mintBlock(*req)

		case ev := <-w.txsCh:
			// Apply transactions to the pending state if we're not mining.
			//
			// Note all transactions received may not be continuous with transactions
			// already included in the current mining block. These transactions will
			// be automatically eliminated.
			if !w.isRunning() && w.current != nil {
				w.mu.RLock()
				coinbase := w.coinbase
				w.mu.RUnlock()

				txs := make(map[common.Address]types.Transactions)
				for _, tx := range ev.Txs {
					acc, _ := types.Sender(w.current.signer, tx)
					txs[acc] = append(txs[acc], tx)
				}
				txset := types.NewTransactionsByPriceAndNonce(w.current.signer, txs)
				//w.commitTransactions(txset, coinbase, nil)
				w.commitTransactions(txset, coinbase)
				w.updateSnapshot()
			} else {
				// If we're mining, but nothing is being processed, wake on new transactions
				/*if w.config.Clique != nil && w.config.Clique.Period == 0 {
					 w.commitNewWork(nil, false, time.Now().Unix())
				}*/
				//TODO: Check here, follow the meitu methods: should not call createNewWork directly:
				// Possible bug here, tx will missing?
				// the whole case can be ignore?
			}
			atomic.AddInt32(&w.newTxs, int32(len(ev.Txs)))

		// System stopped
		case <-w.exitCh:
			return
		case <-w.txsSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		}
	}
}

// taskLoop is a standalone goroutine to fetch sealing task from the generator and
// push them to consensus engine.
func (w *worker) taskLoop() {
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
	for {
		select {
		case task := <-w.taskCh:
			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}
			// Reject duplicate sealing work due to resubmitting.
			sealHash := w.engine.SealHash(task.block.Header())
			if sealHash == prev {
				continue
			}
			// Interrupt previous sealing operation
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash

			if w.skipSealHook != nil && w.skipSealHook(task) {
				continue
			}
			w.pendingMu.Lock()
			w.pendingTasks[w.engine.SealHash(task.block.Header())] = task
			w.pendingMu.Unlock()

			if err := w.engine.Seal(w.chain, task.block, w.resultCh, stopCh); err != nil {
				log.Warn("Block sealing failed", "err", err)
			}
		case <-w.exitCh:
			interrupt()
			return
		}
	}
}

// resultLoop is a standalone goroutine to handle sealing result submitting
// and flush relative data to the database.
func (w *worker) resultLoop() {
	for {
		select {
		case block := <-w.resultCh:
			// Short circuit when receiving empty result.
			if block == nil {
				continue
			}
			// Short circuit when receiving duplicate result caused by resubmitting.
			if w.chain.HasBlock(block.Hash(), block.NumberU64()) {
				continue
			}
			var (
				sealhash = w.engine.SealHash(block.Header())
				hash     = block.Hash()
			)
			w.pendingMu.RLock()
			task, exist := w.pendingTasks[sealhash]
			w.pendingMu.RUnlock()
			if !exist {
				log.Error("Block found but no relative pending task", "number", block.Number(), "sealhash", sealhash, "hash", hash)
				continue
			}
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
				continue
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

			// Insert the block into the set of pending ones to resultLoop for confirmations
			//w.unconfirmed.Insert(block.NumberU64(), block.Hash())

		case <-w.exitCh:
			return
		}
	}
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

// setRecommitInterval updates the interval for miner sealing work recommitting.
func (w *worker) setRecommitInterval(interval time.Duration) {
	//w.resubmitIntervalCh <- interval
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






