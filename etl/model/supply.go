// Copyright (c) 2020-2021 Blockwatch Data Inc.
// Author: alex@blockwatch.cc

package model

import (
	"time"

	"blockwatch.cc/packdb/pack"
	"blockwatch.cc/tzgo/rpc"
	"blockwatch.cc/tzgo/tezos"
)

// Note: removed vesting supply in v9.1, TF vesting time is over
type Supply struct {
	RowId               uint64    `pack:"I,pk,snappy"  json:"row_id"`             // unique id
	Height              int64     `pack:"h,snappy"     json:"height"`             // bc: block height (also for orphans)
	Cycle               int64     `pack:"c,snappy"     json:"cycle"`              // bc: block cycle (tezos specific)
	Timestamp           time.Time `pack:"T,snappy"     json:"time"`               // bc: block creation time
	Total               int64     `pack:"t,snappy"     json:"total"`              // total available supply (including unclaimed)
	Activated           int64     `pack:"A,snappy"     json:"activated"`          // activated fundraiser supply
	Unclaimed           int64     `pack:"U,snappy"     json:"unclaimed"`          // all non-activated fundraiser supply
	Circulating         int64     `pack:"C,snappy"     json:"circulating"`        // (total - unclaimed)
	Liquid              int64     `pack:"L,snappy"     json:"liquid"`             // (total - frozen - unclaimed)
	Delegated           int64     `pack:"E,snappy"     json:"delegated"`          // all delegated balances
	Staking             int64     `pack:"D,snappy"     json:"staking"`            // all delegated + delegate's own balances
	Shielded            int64     `pack:"S,snappy"     json:"shielded"`           // Sapling shielded supply
	ActiveDelegated     int64     `pack:"G,snappy"     json:"active_delegated"`   // delegated  balances to active delegates
	ActiveStaking       int64     `pack:"J,snappy"     json:"active_staking"`     // delegated + delegate's own balances for active delegates
	InactiveDelegated   int64     `pack:"g,snappy"     json:"inactive_delegated"` // delegated  balances to inactive delegates
	InactiveStaking     int64     `pack:"j,snappy"     json:"inactive_staking"`   // delegated + delegate's own balances for inactive delegates
	Minted              int64     `pack:"M,snappy"     json:"minted"`
	MintedBaking        int64     `pack:"b,snappy"     json:"minted_baking"`
	MintedEndorsing     int64     `pack:"e,snappy"     json:"minted_endorsing"`
	MintedSeeding       int64     `pack:"s,snappy"     json:"minted_seeding"`
	MintedAirdrop       int64     `pack:"a,snappy"     json:"minted_airdrop"`
	MintedSubsidy       int64     `pack:"y,snappy"     json:"minted_subsidy"`
	Burned              int64     `pack:"B,snappy"     json:"burned"`
	BurnedDoubleBaking  int64     `pack:"1,snappy"     json:"burned_double_baking"`
	BurnedDoubleEndorse int64     `pack:"2,snappy"     json:"burned_double_endorse"`
	BurnedOrigination   int64     `pack:"3,snappy"     json:"burned_origination"`
	BurnedAllocation    int64     `pack:"4,snappy"     json:"burned_allocation"`
	BurnedSeedMiss      int64     `pack:"5,snappy"     json:"burned_seed_miss"`
	BurnedStorage       int64     `pack:"6,snappy"     json:"burned_storage"`
	BurnedExplicit      int64     `pack:"7,snappy"     json:"burned_explicit"`
	Frozen              int64     `pack:"F,snappy"     json:"frozen"`
	FrozenDeposits      int64     `pack:"d,snappy"     json:"frozen_deposits"`
	FrozenRewards       int64     `pack:"r,snappy"     json:"frozen_rewards"`
	FrozenFees          int64     `pack:"f,snappy"     json:"frozen_fees"`
}

// Ensure Supply implements the pack.Item interface.
var _ pack.Item = (*Supply)(nil)

func (s *Supply) ID() uint64 {
	return s.RowId
}

func (s *Supply) SetID(id uint64) {
	s.RowId = id
}

// be compatible with time series interface
func (s Supply) Time() time.Time {
	return s.Timestamp
}

func (s *Supply) Update(b *Block, delegates map[AccountID]*Account) {
	s.RowId = 0 // force allocating new id
	s.Height = b.Height
	s.Cycle = b.Cycle
	s.Timestamp = b.Timestamp

	// baking rewards
	s.Total += b.Reward - b.BurnedSupply
	s.Minted += b.Reward
	s.FrozenDeposits += b.Deposit - b.UnfrozenDeposits
	s.FrozenRewards += b.Reward - b.UnfrozenRewards
	s.FrozenFees += b.Fee - b.UnfrozenFees

	// overall burn this block
	s.Burned += b.BurnedSupply

	// activated/unclaimed, invoice/airdrop from flows
	for _, f := range b.Flows {
		switch f.Operation {
		case FlowTypeActivation:
			s.Activated += f.AmountIn
			s.Unclaimed -= f.AmountIn
		case FlowTypeNonceRevelation:
			// adjust frozen bucket types to reflect seed burn; note that
			// block.BurnedSupply already contains the sum of all seed burns, so don't
			// double apply
			if f.IsBurned {
				s.BurnedSeedMiss += f.AmountOut
				s.Frozen -= f.AmountOut
				switch f.Category {
				case FlowCategoryRewards:
					s.FrozenRewards -= f.AmountOut
				case FlowCategoryFees:
					s.FrozenFees -= f.AmountOut
				}
			}
		case FlowTypeInvoice, FlowTypeAirdrop:
			s.Total += f.AmountIn
			s.MintedAirdrop += f.AmountIn
			s.Minted += f.AmountIn
		case FlowTypeSubsidy:
			s.Total += f.AmountIn
			s.MintedSubsidy += f.AmountIn
			s.Minted += f.AmountIn
		case FlowTypeDenunciation:
			// moves frozen coins between buckets and owners, burn is already accounted for
			// in block.BurnedSupply, so don't double apply
			switch f.Category {
			case FlowCategoryDeposits:
				s.FrozenDeposits -= f.AmountOut // offender lost deposits
			case FlowCategoryRewards:
				s.FrozenRewards -= f.AmountOut // offender lost rewards
				s.FrozenRewards += f.AmountIn  // accuser reward
			case FlowCategoryFees:
				s.FrozenFees -= f.AmountOut // offender lost fees
			}
		case FlowTypeTransaction:
			// count total Sapling shielded supply across all pools
			if f.IsShielded {
				s.Shielded += f.AmountIn
			}
			if f.IsUnshielded {
				s.Shielded -= f.AmountOut
			}
		}
	}

	// use ops to update non-baking deposits/rewards and individual burn details
	for _, op := range b.Ops {
		switch op.Type {
		case tezos.OpTypeSeedNonceRevelation:
			s.MintedSeeding += op.Reward
			s.Total += op.Reward
			s.Minted += op.Reward
			s.FrozenRewards += op.Reward

		case tezos.OpTypeEndorsement:
			s.MintedEndorsing += op.Reward
			s.Total += op.Reward
			s.Minted += op.Reward
			s.FrozenDeposits += op.Deposit
			s.FrozenRewards += op.Reward

		case tezos.OpTypeDoubleBakingEvidence:
			s.BurnedDoubleBaking += op.Burned

		case tezos.OpTypeDoubleEndorsementEvidence:
			s.BurnedDoubleEndorse += op.Burned

		case tezos.OpTypeOrigination:
			if op.IsSuccess && op.OpL >= 0 {
				storageBurn := b.Params.CostPerByte * op.StoragePaid
				s.BurnedOrigination += op.Burned - storageBurn
				s.BurnedStorage += storageBurn
			}

		case tezos.OpTypeTransaction:
			if op.IsSuccess && op.OpL >= 0 {
				// general burn is already accounted for in block.BurnedSupply
				// here we only assign burn to different reasons
				storageBurn := b.Params.CostPerByte * op.StoragePaid
				s.BurnedAllocation += op.Burned - storageBurn
				s.BurnedStorage += storageBurn
				if oop, ok := b.GetRpcOp(op.OpL, op.OpP, op.OpC); ok {
					tx, _ := oop.(*rpc.TransactionOp)
					if tezos.ZeroAddress.Equal(tx.Destination) {
						s.BurnedExplicit += op.Volume
					}
					for _, iop := range tx.Metadata.InternalResults {
						if iop.OpKind() != tezos.OpTypeTransaction || iop.Destination == nil {
							continue
						}
						if iop.Destination.Equal(tezos.ZeroAddress) {
							s.BurnedExplicit += iop.Amount
						}
					}
				}
			}
		case tezos.OpTypeRegisterConstant:
			s.BurnedStorage += op.Burned
		}
	}
	// update supply totals across all delegates
	s.Staking = 0
	s.Delegated = 0
	s.ActiveStaking = 0
	s.ActiveDelegated = 0
	s.InactiveStaking = 0
	s.InactiveDelegated = 0
	for _, acc := range delegates {
		sb, db := acc.StakingBalance(), acc.DelegatedBalance
		s.Staking += sb
		s.Delegated += db
		if acc.IsActiveDelegate {
			s.ActiveStaking += sb
			s.ActiveDelegated += db
		} else {
			s.InactiveStaking += sb
			s.InactiveDelegated += db
		}
	}

	// add frozen coins
	s.Frozen = s.FrozenDeposits + s.FrozenFees + s.FrozenRewards

	// calculate total baking rewards as difference to total rewards
	s.MintedBaking = s.Minted - s.MintedSeeding - s.MintedEndorsing - s.MintedAirdrop - s.MintedSubsidy

	// general consensus: frozen is part of circulating even though baking
	// rewards are subject to slashing
	s.Circulating = s.Total - s.Unclaimed

	// metric to show how much is economically available for sale with the
	// reasonable assumption that activation (unclaimed) does not move as easily
	s.Liquid = s.Total - s.Frozen - s.Unclaimed
}

func (s *Supply) Rollback(b *Block) {
	// update identity only
	s.Height = b.Height
	s.Cycle = b.Cycle
}
