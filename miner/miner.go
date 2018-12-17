// Copyright 2014 The go-dacc Authors
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

// Package miner implements Ethereum block creation and mining.
package miner

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/daccproject/go-dacc/common"
	"github.com/daccproject/go-dacc/consensus"
	"github.com/daccproject/go-dacc/core"
	"github.com/daccproject/go-dacc/core/state"
	"github.com/daccproject/go-dacc/core/types"
	"github.com/daccproject/go-dacc/eth/downloader"
	"github.com/daccproject/go-dacc/ethdb"
	"github.com/daccproject/go-dacc/event"
	"github.com/daccproject/go-dacc/log"
	"github.com/daccproject/go-dacc/params"
)

// Backend wraps all methods required for mining.
type Backend interface {
	BlockChain() *core.BlockChain
	TxPool() *core.TxPool
	ChainDb() ethdb.Database
}

// Miner creates blocks and searches for proof-of-work values.
type Miner struct {
	mux      *event.TypeMux
	worker   *worker
	coinbase common.Address
	eth      Backend
	engine   consensus.Engine
	exitCh   chan struct{}

	canStart    int32 // can start indicates whether we can start the mining operation
	shouldStart int32 // should start indicates whether we should start after sync
}

func New(eth Backend, config *params.ChainConfig, mux *event.TypeMux, engine consensus.Engine, recommit time.Duration, gasFloor, gasCeil uint64) *Miner {
	miner := &Miner{
		eth:      eth,
		mux:      mux,
		engine:   engine,
		exitCh:   make(chan struct{}),
		worker:   newWorker(config, engine, eth, mux, recommit, gasFloor, gasCeil),
		canStart: 1,
	}
	go miner.update()

	return miner
}

// update keeps track of the downloader events. Please be aware that this is a one shot type of update loop.
// It's entered once and as soon as `Done` or `Failed` has been broadcasted the events are unregistered and
// the loop is exited. This to prevent a major security vuln where external parties can DOS you with blocks
// and halt your mining operation for as long as the DOS continues.
func (m *Miner) update() {
	events := m.mux.Subscribe(downloader.StartEvent{}, downloader.DoneEvent{}, downloader.FailedEvent{})
	defer events.Unsubscribe()

	for {
		select {
		case ev := <-events.Chan():
			if ev == nil {
				return
			}
			switch ev.Data.(type) {
			case downloader.StartEvent:
				atomic.StoreInt32(&m.canStart, 0)
				if m.Mining() {
					m.Stop()
					atomic.StoreInt32(&m.shouldStart, 1)
					log.Info("Mining aborted due to sync")
				}
			case downloader.DoneEvent, downloader.FailedEvent:
				shouldStart := atomic.LoadInt32(&m.shouldStart) == 1

				atomic.StoreInt32(&m.canStart, 1)
				atomic.StoreInt32(&m.shouldStart, 0)
				if shouldStart {
					m.Start(m.coinbase)
				}
				// stop immediately and ignore all further pending events
				return
			}
		case <-m.exitCh:
			return
		}
	}
}

func (m *Miner) Start(coinbase common.Address) {
	
	atomic.StoreInt32(&m.shouldStart, 1)
	//m.SetEtherbase(coinbase)
	m.SetCoinbase(coinbase)

	if atomic.LoadInt32(&m.canStart) == 0 {
		log.Info("Network syncing, will start miner afterwards")
		return
	}
	log.Info("miner start .....")
	m.worker.start()
}

func (m *Miner) Stop() {
	m.worker.stop()
	atomic.StoreInt32(&m.shouldStart, 0)
}

func (m *Miner) Close() {
	m.worker.close()
	close(m.exitCh)
}

func (m *Miner) Mining() bool {
	return m.worker.isRunning()
}

func (m *Miner) HashRate() uint64 {
	if pow, ok := m.engine.(consensus.PoW); ok {
		return uint64(pow.Hashrate())
	}
	return 0
}

func (m *Miner) SetExtra(extra []byte) error {
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		return fmt.Errorf("Extra exceeds max length. %d > %v", len(extra), params.MaximumExtraDataSize)
	}
	m.worker.setExtra(extra)
	return nil
}

// SetRecommitInterval sets the interval for sealing work resubmitting.
func (m *Miner) SetRecommitInterval(interval time.Duration) {
	//m.worker.setRecommitInterval(interval)
}

// Pending returns the currently pending block and associated state.
func (m *Miner) Pending() (*types.Block, *state.StateDB) {
	return m.worker.pending()
}

// PendingBlock returns the currently pending block.
//
// Note, to access both the pending block and the pending state
// simultaneously, please use Pending(), as the pending state can
// change between multiple method calls
func (m *Miner) PendingBlock() *types.Block {
	return m.worker.pendingBlock()
}

func (m *Miner) SetCoinbase(addr common.Address) {
	m.coinbase = addr
	m.worker.setCoinbase(addr)
}
