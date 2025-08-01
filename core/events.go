// Copyright 2014 The go-ethereum Authors
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

package core

import (
	"github.com/ethereum/go-ethereum/core/types"
)

// NewTxsEvent is posted when a batch of transactions enters the transaction pool.
type NewTxsEvent struct{ Txs []*types.Transaction }

// ReannoTxsEvent is posted when a batch of local pending transactions exceed a specified duration.
type ReannoTxsEvent struct{ Txs []*types.Transaction }

// NewSealedBlockEvent is posted when a block has been sealed.
type NewSealedBlockEvent struct{ Block *types.Block }

// NewMinedBlockEvent is posted when a block has been mined.
type NewMinedBlockEvent struct{ Block *types.Block }

// RemovedLogsEvent is posted when a reorg happens
type RemovedLogsEvent struct{ Logs []*types.Log }

// NewVoteEvent is posted when a batch of votes enters the vote pool.
type NewVoteEvent struct{ Vote *types.VoteEnvelope }

// FinalizedHeaderEvent is posted when a finalized header is reached.
type FinalizedHeaderEvent struct{ Header *types.Header }

type ChainEvent struct {
	Header *types.Header
}

type ChainHeadEvent struct {
	Header *types.Header
}

type HighestVerifiedBlockEvent struct{ Header *types.Header }
