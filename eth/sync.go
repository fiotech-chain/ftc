// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

const (
	forceSyncCycle      = 10 * time.Second // Time interval to force syncs, even if few peers are available
	defaultMinSyncPeers = 5                // Amount of peers desired to start syncing
)

// syncTransactions starts sending all currently pending transactions to the given peer.
func (h *handler) syncTransactions(p *eth.Peer) {
	var hashes []common.Hash
	for _, batch := range h.txpool.Pending(txpool.PendingFilter{OnlyPlainTxs: true}) {
		for _, tx := range batch {
			hashes = append(hashes, tx.Hash)
		}
	}
	if len(hashes) == 0 {
		return
	}
	p.AsyncSendPooledTransactionHashes(hashes)
}

// syncVotes starts sending all currently pending votes to the given peer.
func (h *handler) syncVotes(p *bscPeer) {
	votes := h.votepool.GetVotes()
	if len(votes) == 0 {
		return
	}
	p.AsyncSendVotes(votes)
}

// chainSyncer coordinates blockchain sync components.
type chainSyncer struct {
	handler     *handler
	force       *time.Timer
	forced      bool // true when force timer fired
	warned      time.Time
	peerEventCh chan struct{}
	doneCh      chan error // non-nil when sync is running
}

// chainSyncOp is a scheduled sync operation.
type chainSyncOp struct {
	mode downloader.SyncMode
	peer *eth.Peer
	td   *big.Int
	head common.Hash
}

// newChainSyncer creates a chainSyncer.
func newChainSyncer(handler *handler) *chainSyncer {
	return &chainSyncer{
		handler:     handler,
		peerEventCh: make(chan struct{}),
	}
}

// handlePeerEvent notifies the syncer about a change in the peer set.
// This is called for new peers and every time a peer announces a new
// chain head.
func (cs *chainSyncer) handlePeerEvent() bool {
	select {
	case cs.peerEventCh <- struct{}{}:
		return true
	case <-cs.handler.quitSync:
		return false
	}
}

// loop runs in its own goroutine and launches the sync when necessary.
func (cs *chainSyncer) loop() {
	defer cs.handler.wg.Done()

	cs.handler.blockFetcher.Start()
	cs.handler.txFetcher.Start()
	defer cs.handler.blockFetcher.Stop()
	defer cs.handler.txFetcher.Stop()
	defer cs.handler.downloader.Terminate()

	// The force timer lowers the peer count threshold down to one when it fires.
	// This ensures we'll always start sync even if there aren't enough peers.
	cs.force = time.NewTimer(forceSyncCycle)
	defer cs.force.Stop()

	for {
		if op := cs.nextSyncOp(); op != nil {
			cs.startSync(op)
		}
		select {
		case <-cs.peerEventCh:
			// Peer information changed, recheck.
		case <-cs.doneCh:
			cs.doneCh = nil
			cs.force.Reset(forceSyncCycle)
			cs.forced = false
		case <-cs.force.C:
			cs.forced = true

		case <-cs.handler.quitSync:
			// Disable all insertion on the blockchain. This needs to happen before
			// terminating the downloader because the downloader waits for blockchain
			// inserts, and these can take a long time to finish.
			cs.handler.chain.StopInsert()
			cs.handler.downloader.Terminate()
			if cs.doneCh != nil {
				<-cs.doneCh
			}
			return
		}
	}
}

// nextSyncOp determines whether sync is required at this time.
func (cs *chainSyncer) nextSyncOp() *chainSyncOp {
	if cs.doneCh != nil {
		return nil // Sync already running
	}
	// Ensure we're at minimum peer count.
	minPeers := defaultMinSyncPeers
	if cs.forced {
		minPeers = 1
	} else if minPeers > cs.handler.maxPeers {
		minPeers = cs.handler.maxPeers
	}
	if cs.handler.peers.len() < minPeers {
		return nil
	}
	// We have enough peers, pick the one with the highest TD, but avoid going
	// over the terminal total difficulty. Above that we expect the consensus
	// clients to direct the chain head to sync to.
	peer := cs.handler.peers.peerWithHighestTD()
	if peer == nil {
		return nil
	}
	mode, ourTD := cs.modeAndLocalHead()
	op := peerToSyncOp(mode, peer)
	if op.td.Cmp(ourTD) <= 0 {
		if !cs.handler.acceptTxs.Load() {
			// Occurs only during a quick restart.
			cs.handler.acceptTxs.Store(true)
			log.Info("Enable transaction acceptance for already in sync.")
		}
		// We seem to be in sync according to the legacy rules. In the merge
		// world, it can also mean we're stuck on the merge block, waiting for
		// a beacon client. In the latter case, notify the user.
		if ttd := cs.handler.chain.Config().TerminalTotalDifficulty; ttd != nil && ourTD.Cmp(ttd) >= 0 && time.Since(cs.warned) > 10*time.Second {
			log.Warn("Local chain is post-merge, waiting for beacon client sync switch-over...")
			cs.warned = time.Now()
		}
		return nil // We're in sync
		// } else if op.td.Cmp(new(big.Int).Add(ourTD, new(big.Int).SetUint64(10*2))) > 0 {
		// 	if cs.handler.acceptTxs.Load() && rand.New(rand.NewSource(time.Now().UnixNano())).Int31n(10) < 1 {
		// 		// There is only a 1/10 probability of disabling transaction acceptance.
		// 		// This randomness helps protect against attacks where a malicious node falsely claims to have higher blocks.
		// 		cs.handler.acceptTxs.Store(false)
		// 		log.Info("Disable transaction acceptance randomly for the delay exceeding 10 blocks.")
		// 	}
	} else if op.td.Cmp(new(big.Int).Add(ourTD, common.Big2)) <= 0 { // common.Big2: difficulty of an in-turn block
		// On BSC, blocks are produced much faster than on Ethereum.
		// If the node is only slightly behind (e.g., 1 block), syncing is unnecessary.
		// It's likely still processing broadcasted blocks or block hash announcements.
		// In most cases, the node will catch up within 3 seconds.
		time.Sleep(3 * time.Second)

		// Re-check local head to see if it has caught up
		if _, latestTD := cs.modeAndLocalHead(); ourTD.Cmp(latestTD) < 0 {
			log.Trace("The local head is already caught up; synchronization is not required.")
			return nil
		}
	}

	return op
}

func peerToSyncOp(mode downloader.SyncMode, p *eth.Peer) *chainSyncOp {
	peerHead, peerTD := p.Head()
	return &chainSyncOp{mode: mode, peer: p, td: peerTD, head: peerHead}
}

func (cs *chainSyncer) modeAndLocalHead() (downloader.SyncMode, *big.Int) {
	// If we're in snap sync mode, return that directly
	if cs.handler.snapSync.Load() {
		block := cs.handler.chain.CurrentSnapBlock()
		td := cs.handler.chain.GetTd(block.Hash(), block.Number.Uint64())
		return ethconfig.SnapSync, td
	}
	// We are probably in full sync, but we might have rewound to before the
	// snap sync pivot, check if we should re-enable snap sync.
	head := cs.handler.chain.CurrentBlock()
	if pivot := rawdb.ReadLastPivotNumber(cs.handler.database); pivot != nil {
		if head.Number.Uint64() < *pivot {
			block := cs.handler.chain.CurrentSnapBlock()
			td := cs.handler.chain.GetTd(block.Hash(), block.Number.Uint64())
			return ethconfig.SnapSync, td
		}
	}
	// We are in a full sync, but the associated head state is missing. To complete
	// the head state, forcefully rerun the snap sync. Note it doesn't mean the
	// persistent state is corrupted, just mismatch with the head block.
	if !cs.handler.chain.NoTries() && !cs.handler.chain.HasState(head.Root) {
		block := cs.handler.chain.CurrentSnapBlock()
		td := cs.handler.chain.GetTd(block.Hash(), block.Number.Uint64())
		log.Info("Reenabled snap sync as chain is stateless")
		return ethconfig.SnapSync, td
	}
	// Nope, we're really full syncing
	td := cs.handler.chain.GetTd(head.Hash(), head.Number.Uint64())
	return ethconfig.FullSync, td
}

// startSync launches doSync in a new goroutine.
func (cs *chainSyncer) startSync(op *chainSyncOp) {
	cs.doneCh = make(chan error, 1)
	go func() { cs.doneCh <- cs.handler.doSync(op) }()
}

// doSync synchronizes the local blockchain with a remote peer.
func (h *handler) doSync(op *chainSyncOp) error {
	// Run the sync cycle, and disable snap sync if we're past the pivot block
	err := h.downloader.LegacySync(op.peer.ID(), op.head, op.peer.Name(), op.td, h.chain.Config().TerminalTotalDifficulty, op.mode)
	if err != nil {
		return err
	}
	h.enableSyncedFeatures()

	head := h.chain.CurrentBlock()
	if head.Number.Uint64() > 0 {
		// We've completed a sync cycle, notify all peers of new state. This path is
		// essential in star-topology networks where a gateway node needs to notify
		// all its out-of-date peers of the availability of a new block. This failure
		// scenario will most often crop up in private and hackathon networks with
		// degenerate connectivity, but it should be healthy for the mainnet too to
		// more reliably update peers or the local TD state.
		if block := h.chain.GetBlock(head.Hash(), head.Number.Uint64()); block != nil {
			h.BroadcastBlock(block, false)
		}
	}
	return nil
}
