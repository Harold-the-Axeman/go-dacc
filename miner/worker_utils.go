package miner

import (
	"github.com/daccproject/go-dacc/core/vm"
	"math/big"
	"time"

	"github.com/daccproject/go-dacc/consensus/dpos"
	"github.com/daccproject/go-dacc/common"
	"github.com/daccproject/go-dacc/core"
	"github.com/daccproject/go-dacc/core/types"
	"github.com/daccproject/go-dacc/log"
	"github.com/daccproject/go-dacc/params"
)

// Modified by Harold, move the code in the end to other loops.
func (self *worker) mintBlock(timestamp int64) {

	engine, ok := self.engine.(*dpos.Dpos)
	if !ok {
		log.Error("Only the dpos engine was allowed")
		return
	}
	err := engine.CheckValidator(self.chain.CurrentBlock(), timestamp)
	if err != nil {
		switch err {
		case dpos.ErrWaitForPrevBlock,
			dpos.ErrMintFutureBlock,
			dpos.ErrInvalidBlockValidator,
			dpos.ErrInvalidMintBlockTime:
			log.Debug("Failed to mint the block, while ", "err", err)
		default:
			log.Error("Failed to mint the block", "err", err)
		}
		return
	}
	self.createNewWork(timestamp)

}

// commitNewWork generates several new sealing tasks based on the parent block.
func (w *worker) createNewWork(timestamp int64) {

	w.mu.RLock()
	defer w.mu.RUnlock()

	tstart := time.Now()
	parent := w.chain.CurrentBlock()

	if parent.Time().Cmp(new(big.Int).SetInt64(timestamp)) >= 0 {
		//timestamp = parent.Time().Int64() + 1
		log.Error("Miner: task timestamp larger than current header's timestamp")
		return
	}

	num := parent.Number()
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     num.Add(num, common.Big1),
		GasLimit:   core.CalcGasLimit(parent, w.gasFloor, w.gasCeil),
		Extra:      w.extra,
		Time:       big.NewInt(timestamp),
	}
	// Only set the coinbase if our consensus engine is running (avoid spurious block rewards)
	if w.isRunning() {
		if w.coinbase == (common.Address{}) {
			log.Error("Refusing to mine without etherbase")
			return
		}
		header.Coinbase = w.coinbase
	}
	if err := w.engine.Prepare(w.chain, header); err != nil {
		log.Error("Failed to prepare header for mining", "err", err)
		return
	}

	err := w.makeCurrent(parent, header)
	if err != nil {
		log.Error("Failed to create mining context", "err", err)
		return
	}
	// Create the current work task and check any fork transitions needed
	env := w.current

	// Fill the block with all available pending transactions.
	pending, err := w.eth.TxPool().Pending()
	if err != nil {
		log.Error("Failed to fetch pending transactions", "err", err)
		return
	}

	// Short circuit if there is no available pending transactions
	// Split the pending transactions into locals and remotes
	localTxs, remoteTxs := make(map[common.Address]types.Transactions), pending
	for _, account := range w.eth.TxPool().Locals() {
		if txs := remoteTxs[account]; len(txs) > 0 {
			delete(remoteTxs, account)
			localTxs[account] = txs
		}
	}
	if len(localTxs) > 0 {
		txs := types.NewTransactionsByPriceAndNonce(env.signer, localTxs)
		//if w.commitTransactions(txs, w.coinbase, interrupt) {
		if w.commitTransactions(txs, w.coinbase) {
			return
		}
	}
	if len(remoteTxs) > 0 {
		txs := types.NewTransactionsByPriceAndNonce(env.signer, remoteTxs)
		//if w.commitTransactions(txs, w.coinbase, interrupt) {
		if w.commitTransactions(txs, w.coinbase) {
			return
		}

	}
	w.commit(tstart)
}

// commit runs any post-transaction state modifications, assembles the final block
// and commits new work if consensus engine is running.
//func (w *worker) commit(uncles []*types.Header, interval func(), update bool, start time.Time) error {
func (w *worker) commit(start time.Time) error {
	// Deep copy receipts here to avoid interaction between different tasks.
	receipts := make([]*types.Receipt, len(w.current.receipts))
	for i, l := range w.current.receipts {
		receipts[i] = new(types.Receipt)
		*receipts[i] = *l
	}
	s := w.current.state.Copy()
	dc := w.current.dposContext.Copy()

	block, err := w.engine.Finalize(w.chain, w.current.header, s, w.current.txs, nil, w.current.receipts, dc)
	if err != nil {
		log.Info("worker.commit.finalize", err.Error())
		return err
	}
	//NOTE: in commitNewWork of the v1.7.3 version, Harold
	block.DposContext = dc

	if w.isRunning() {
		task := &task{receipts: receipts, state: s, block: block, createdAt: time.Now()}
		//w.unconfirmed.Shift(block.NumberU64() - 1)
		w.commitTask(task)

		// Logging
		feesWei := new(big.Int)
		for i, tx := range block.Transactions() {
			feesWei.Add(feesWei, new(big.Int).Mul(new(big.Int).SetUint64(receipts[i].GasUsed), tx.GasPrice()))
		}
		feesEth := new(big.Float).Quo(new(big.Float).SetInt(feesWei), new(big.Float).SetInt(big.NewInt(params.Ether)))

		log.Info("⚡️ Commit new mining work", "number", block.Number(), "sealhash", w.engine.SealHash(block.Header()),
			"txs", w.current.tcount, "gas", block.GasUsed(), "fees", feesEth, "elapsed", common.PrettyDuration(time.Since(start)))

	}
	//TODO: have to wait above to finish, async execution?
	w.updateSnapshot()
	return nil
}

// makeCurrent creates a new environment for the current cycle.
func (w *worker) makeCurrent(parent *types.Block, header *types.Header) error {
	state, err := w.chain.StateAt(parent.Root())
	if err != nil {
		return err
	}
	// Add by Shara
	dposContext, err := types.NewDposContextFromProto(w.eth.ChainDb(), parent.Header().DposContext)
	if err != nil {
		return err
	}

	// End add by Shara
	env := &environment{
		signer: types.NewEIP155Signer(w.config.ChainID),
		state:  state,
		dposContext: dposContext,
		header: header,
	}
	// Keep track of transactions which return errors so they can be removed
	env.tcount = 0
	w.current = env
	return nil
}

// updateSnapshot updates pending snapshot block and state.
// Note this function assumes the current variable is thread safe.
func (w *worker) updateSnapshot() {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = types.NewBlock(
		w.current.header,
		w.current.txs,
		nil,
		w.current.receipts,
	)

	w.snapshotState = w.current.state.Copy()
}

func (w *worker) commitTransaction(tx *types.Transaction, coinbase common.Address) ([]*types.Log, error) {
	snap := w.current.state.Snapshot()
	// Change by Shara
	dposSnap := w.current.dposContext.Snapshot()

	receipt, _, err := core.ApplyTransaction(w.config, w.current.dposContext, w.chain, &coinbase, w.current.gasPool, w.current.state, w.current.header, tx, &w.current.header.GasUsed, vm.Config{})
	// End change by Shara
	if err != nil {
		w.current.state.RevertToSnapshot(snap)
		// Add by Shara
		w.current.dposContext.RevertToSnapShot(dposSnap)
		// End add by Shara
		return nil, err
	}
	w.current.txs = append(w.current.txs, tx)
	w.current.receipts = append(w.current.receipts, receipt)

	return receipt.Logs, nil
}

// interrupt: nil in txsCh, commitInterruptNone in ticker ?
//func (w *worker) commitTransactions(txs *types.TransactionsByPriceAndNonce, coinbase common.Address, interrupt *int32) bool {
func (w *worker) commitTransactions(txs *types.TransactionsByPriceAndNonce, coinbase common.Address) bool {
	// Short circuit if current is nil
	if w.current == nil {
		return true
	}

	if w.current.gasPool == nil {
		w.current.gasPool = new(core.GasPool).AddGas(w.current.header.GasLimit)
	}

	var coalescedLogs []*types.Log

	for {
		// In the following three cases, we will interrupt the execution of the transaction.
		// (1) new head block event arrival, the interrupt signal is 1
		// (2) worker start or restart, the interrupt signal is 1
		// (3) worker recreate the mining block with any newly arrived transactions, the interrupt signal is 2.
		// For the first two cases, the semi-finished work will be discarded.
		// For the third case, the semi-finished work will be submitted to the consensus engine.

		// If we don't have enough gas for any further transactions then we're done
		if w.current.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", w.current.gasPool, "want", params.TxGas)
			break
		}
		// Retrieve the next transaction and abort if all done
		tx := txs.Peek()
		if tx == nil {
			break
		}
		// Error may be ignored here. The error has already been checked
		// during transaction acceptance is the transaction pool.
		//
		// We use the eip155 signer regardless of the current hf.
		from, _ := types.Sender(w.current.signer, tx)
		// Check whether the tx is replay protected. If we're not in the EIP155 hf
		// phase, start ignoring the sender until we do.
		if tx.Protected() && !w.config.IsEIP155(w.current.header.Number) {
			log.Trace("Ignoring reply protected transaction", "hash", tx.Hash(), "eip155", w.config.EIP155Block)

			txs.Pop()
			continue
		}
		// Start executing the transaction
		w.current.state.Prepare(tx.Hash(), common.Hash{}, w.current.tcount)

		logs, err := w.commitTransaction(tx, coinbase)
		switch err {
		case core.ErrGasLimitReached:
			// Pop the current out-of-gas transaction without shifting in the next from the account
			log.Trace("Gas limit exceeded for current block", "sender", from)
			txs.Pop()

		case core.ErrNonceTooLow:
			// New head notification data race between the transaction pool and miner, shift
			log.Trace("Skipping transaction with low nonce", "sender", from, "nonce", tx.Nonce())
			txs.Shift()

		case core.ErrNonceTooHigh:
			// Reorg notification data race between the transaction pool and miner, skip account =
			log.Trace("Skipping account with hight nonce", "sender", from, "nonce", tx.Nonce())
			txs.Pop()

		case nil:
			// Everything ok, collect the logs and shift in the next transaction from the same account
			coalescedLogs = append(coalescedLogs, logs...)
			w.current.tcount++
			txs.Shift()

		default:
			// Strange error, discard the transaction and get the next in line (note, the
			// nonce-too-high clause will prevent us from executing in vain).
			log.Debug("Transaction failed, account skipped", "hash", tx.Hash(), "err", err)
			txs.Shift()
		}
	}

	if !w.isRunning() && len(coalescedLogs) > 0 {
		// We don't push the pendingLogsEvent while we are mining. The reason is that
		// when we are mining, the worker will regenerate a mining block every 3 seconds.
		// In order to avoid pushing the repeated pendingLog, we disable the pending log pushing.

		// make a copy, the state caches the logs and these logs get "upgraded" from pending to mined
		// logs by filling in the block hash when the block was mined by the local miner. This can
		// cause a race condition if a log was "upgraded" before the PendingLogsEvent is processed.
		cpy := make([]*types.Log, len(coalescedLogs))
		for i, l := range coalescedLogs {
			cpy[i] = new(types.Log)
			*cpy[i] = *l
		}
		go w.mux.Post(core.PendingLogsEvent{Logs: cpy})
	}
	return false
}

