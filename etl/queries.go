// Copyright (c) 2020-2022 Blockwatch Data Inc.
// Author: alex@blockwatch.cc

package etl

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"blockwatch.cc/packdb/pack"
	"blockwatch.cc/packdb/util"
	"blockwatch.cc/tzgo/micheline"
	"blockwatch.cc/tzgo/tezos"
	"blockwatch.cc/tzindex/etl/index"
	"blockwatch.cc/tzindex/etl/model"
)

type ListRequest struct {
	Account     *model.Account
	Mode        pack.FilterMode
	Typs        model.OpTypeList
	Since       int64
	Until       int64
	Offset      uint
	Limit       uint
	Cursor      uint64
	Order       pack.OrderType
	SenderId    model.AccountID
	ReceiverId  model.AccountID
	Entrypoints []int64
	Period      int64
	BigmapId    int64
	BigmapKey   tezos.ExprHash
	OpId        model.OpID
	WithStorage bool
}

func (r ListRequest) WithDelegation() bool {
	if r.Mode == pack.FilterModeEqual || r.Mode == pack.FilterModeIn {
		for _, t := range r.Typs {
			if t == model.OpTypeDelegation {
				return true
			}
		}
		return false
	} else {
		for _, t := range r.Typs {
			if t == model.OpTypeDelegation {
				return false
			}
		}
		return true
	}
}

func (m *Indexer) ChainByHeight(ctx context.Context, height int64) (*model.Chain, error) {
	table, err := m.Table(index.ChainTableKey)
	if err != nil {
		return nil, err
	}
	c := &model.Chain{}
	err = pack.NewQuery("chain_by_height", table).
		AndEqual("height", height).
		WithLimit(1).
		Execute(ctx, c)
	if err != nil {
		return nil, err
	}
	if c.RowId == 0 {
		return nil, index.ErrNoChainEntry
	}
	return c, nil
}

func (m *Indexer) SupplyByHeight(ctx context.Context, height int64) (*model.Supply, error) {
	table, err := m.Table(index.SupplyTableKey)
	if err != nil {
		return nil, err
	}
	s := &model.Supply{}
	err = pack.NewQuery("supply_by_height", table).
		AndEqual("height", height).
		WithLimit(1).
		Execute(ctx, s)
	if err != nil {
		return nil, err
	}
	if s.RowId == 0 {
		return nil, index.ErrNoSupplyEntry
	}
	return s, nil
}

func (m *Indexer) SupplyByTime(ctx context.Context, t time.Time) (*model.Supply, error) {
	table, err := m.Table(index.SupplyTableKey)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	from, to := t, now
	if from.After(to) {
		from, to = to, from
	}
	s := &model.Supply{}
	err = pack.NewQuery("supply_by_time", table).
		AndRange("time", from, to). // search for timestamp
		AndGte("height", 1).        // height larger than supply init block 1
		WithLimit(1).
		Execute(ctx, s)
	if err != nil {
		return nil, err
	}
	if s.RowId == 0 {
		return nil, index.ErrNoSupplyEntry
	}
	return s, nil
}

type Growth struct {
	NewAccounts     int64
	NewContracts    int64
	ClearedAccounts int64
	FundedAccounts  int64
}

func (m *Indexer) GrowthByDuration(ctx context.Context, to time.Time, d time.Duration) (*Growth, error) {
	table, err := m.Table(index.BlockTableKey)
	if err != nil {
		return nil, err
	}
	type XBlock struct {
		NewAccounts     int64 `pack:"A"`
		NewContracts    int64 `pack:"C"`
		ClearedAccounts int64 `pack:"E"`
		FundedAccounts  int64 `pack:"J"`
	}
	from := to.Add(-d)
	x := &XBlock{}
	g := &Growth{}
	err = pack.NewQuery("aggregate_growth", table).
		WithFields("A", "C", "E", "J").
		AndRange("time", from, to). // search for timestamp
		Stream(ctx, func(r pack.Row) error {
			if err := r.Decode(x); err != nil {
				return err
			}
			g.NewAccounts += x.NewAccounts
			g.NewContracts += x.NewContracts
			g.ClearedAccounts += x.ClearedAccounts
			g.FundedAccounts += x.FundedAccounts
			return nil
		})
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (m *Indexer) BlockByID(ctx context.Context, id uint64) (*model.Block, error) {
	if id == 0 {
		return nil, index.ErrNoBlockEntry
	}
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return nil, err
	}
	b := &model.Block{}
	err = pack.NewQuery("block_by_id", blocks).
		AndEqual("I", id).
		Execute(ctx, b)
	if err != nil {
		return nil, err
	}
	if b.RowId == 0 {
		return nil, index.ErrNoBlockEntry
	}
	b.Params, _ = m.reg.GetParamsByDeployment(b.Version)
	return b, nil
}

// find a block's canonical successor (non-orphan)
func (m *Indexer) BlockByParentId(ctx context.Context, id uint64) (*model.Block, error) {
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return nil, err
	}
	b := &model.Block{}
	err = pack.NewQuery("block_by_parent_id", blocks).
		AndEqual("parent_id", id).
		WithLimit(1).
		Execute(ctx, b)
	if err != nil {
		return nil, err
	}
	if b.RowId == 0 {
		return nil, index.ErrNoBlockEntry
	}
	b.Params, _ = m.reg.GetParamsByDeployment(b.Version)
	return b, nil
}

func (m *Indexer) BlockHashByHeight(ctx context.Context, height int64) (tezos.BlockHash, error) {
	type XBlock struct {
		Hash tezos.BlockHash `pack:"H"`
	}
	b := &XBlock{}
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return b.Hash, err
	}
	err = pack.NewQuery("block_hash_by_height", blocks).
		AndEqual("height", height).
		WithLimit(1).
		Execute(ctx, b)
	if err != nil {
		return b.Hash, err
	}
	if !b.Hash.IsValid() {
		return b.Hash, index.ErrNoBlockEntry
	}
	return b.Hash, nil
}

func (m *Indexer) BlockHashById(ctx context.Context, id uint64) (tezos.BlockHash, error) {
	type XBlock struct {
		Hash tezos.BlockHash `pack:"H"`
	}
	b := &XBlock{}
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return b.Hash, err
	}
	err = pack.NewQuery("block_hash_by_id", blocks).
		WithFields("H").
		AndEqual("I", id).
		Execute(ctx, b)
	if err != nil {
		return b.Hash, err
	}
	if !b.Hash.IsValid() {
		return b.Hash, index.ErrNoBlockEntry
	}
	return b.Hash, nil
}

func (m *Indexer) BlockByHeight(ctx context.Context, height int64) (*model.Block, error) {
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return nil, err
	}
	b := &model.Block{}
	err = pack.NewQuery("block_by_height", blocks).
		AndEqual("height", height).
		Execute(ctx, b)
	if err != nil {
		return nil, err
	}
	if b.RowId == 0 {
		return nil, index.ErrNoBlockEntry
	}
	b.Params, _ = m.reg.GetParamsByDeployment(b.Version)
	return b, nil
}

func (m *Indexer) BlockByHash(ctx context.Context, h tezos.BlockHash, from, to int64) (*model.Block, error) {
	if !h.IsValid() {
		return nil, fmt.Errorf("invalid block hash %s", h)
	}
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("block_by_hash", blocks).WithLimit(1).WithDesc()
	if from > 0 {
		q = q.AndGte("height", from)
	}
	if to > 0 {
		q = q.AndLte("height", to)
	}
	// most expensive condition last
	q = q.AndEqual("hash", h.Hash.Hash[:])
	b := &model.Block{}
	if err = q.Execute(ctx, b); err != nil {
		return nil, err
	}
	if b.RowId == 0 {
		return nil, index.ErrNoBlockEntry
	}
	b.Params, _ = m.reg.GetParamsByDeployment(b.Version)
	return b, nil
}

func (m *Indexer) LookupBlockId(ctx context.Context, blockIdent string) (tezos.BlockHash, int64, error) {
	switch true {
	case blockIdent == "head":
		b, err := m.BlockByHeight(ctx, m.tips[index.BlockTableKey].Height)
		if err != nil {
			return tezos.BlockHash{}, 0, err
		}
		return b.Hash, b.Height, nil
	case len(blockIdent) == tezos.HashTypeBlock.Base58Len() || tezos.HashTypeBlock.MatchPrefix(blockIdent):
		// assume it's a hash
		var blockHash tezos.BlockHash
		blockHash, err := tezos.ParseBlockHash(blockIdent)
		if err != nil {
			return tezos.BlockHash{}, 0, index.ErrInvalidBlockHash
		}
		b, err := m.BlockByHash(ctx, blockHash, 0, 0)
		return b.Hash, b.Height, nil
	default:
		// try parsing as height
		var blockHeight int64
		blockHeight, err := strconv.ParseInt(blockIdent, 10, 64)
		if err != nil {
			return tezos.BlockHash{}, 0, index.ErrInvalidBlockHeight
		}
		// from cache
		return m.LookupBlockHash(ctx, blockHeight), blockHeight, nil
	}
}

func (m *Indexer) LookupBlock(ctx context.Context, blockIdent string) (*model.Block, error) {
	var (
		b   *model.Block
		err error
	)
	switch true {
	case blockIdent == "head":
		b, err = m.BlockByHeight(ctx, m.tips[index.BlockTableKey].Height)
	case len(blockIdent) == tezos.HashTypeBlock.Base58Len() || tezos.HashTypeBlock.MatchPrefix(blockIdent):
		// assume it's a hash
		var blockHash tezos.BlockHash
		blockHash, err = tezos.ParseBlockHash(blockIdent)
		if err != nil {
			return nil, index.ErrInvalidBlockHash
		}
		b, err = m.BlockByHash(ctx, blockHash, 0, 0)
	default:
		// try parsing as height
		var blockHeight int64
		blockHeight, err = strconv.ParseInt(blockIdent, 10, 64)
		if err != nil {
			return nil, index.ErrInvalidBlockHeight
		}
		b, err = m.BlockByHeight(ctx, blockHeight)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (m *Indexer) LookupLastBakedBlock(ctx context.Context, bkr *model.Baker) (*model.Block, error) {
	if bkr.BlocksBaked == 0 {
		return nil, index.ErrNoBlockEntry
	}
	blocks, err := m.Table(index.BlockTableKey)
	if err != nil {
		return nil, err
	}
	b := &model.Block{}
	err = pack.NewQuery("last_baked_block", blocks).
		WithLimit(1).
		WithDesc().
		AndRange("height", bkr.Account.FirstSeen, bkr.Account.LastSeen).
		AndEqual("proposer_id", bkr.AccountId).
		Execute(ctx, b)
	if err != nil {
		return nil, err
	}
	if b.RowId == 0 {
		return nil, index.ErrNoBlockEntry
	}
	return b, nil
}

func (m *Indexer) LookupLastEndorsedBlock(ctx context.Context, bkr *model.Baker) (*model.Block, error) {
	if bkr.SlotsEndorsed == 0 {
		return nil, index.ErrNoBlockEntry
	}
	ops, err := m.Table(index.EndorseOpTableKey)
	if err != nil {
		return nil, err
	}
	var ed model.Endorsement
	err = pack.NewQuery("last_endorse_op", ops).
		WithFields("h").
		WithLimit(1).
		WithDesc().
		AndRange("height", bkr.Account.FirstSeen, bkr.Account.LastSeen).
		AndEqual("sender_id", bkr.AccountId).
		Execute(ctx, &ed)
	if err != nil {
		return nil, err
	}
	if ed.Height == 0 {
		return nil, index.ErrNoBlockEntry
	}
	return m.BlockByHeight(ctx, ed.Height)
}

func (m *Indexer) ListBlockRights(ctx context.Context, height int64, typ tezos.RightType) ([]model.BaseRight, error) {
	rights, err := m.Table(index.RightsTableKey)
	if err != nil {
		return nil, err
	}
	p := m.ParamsByHeight(height)
	q := pack.NewQuery("list_rights", rights).
		AndEqual("cycle", p.CycleFromHeight(height))
	if typ.IsValid() {
		q = q.AndEqual("type", typ)
	}
	resp := make([]model.BaseRight, 0)
	right := model.Right{}
	start := p.CycleStartHeight(p.CycleFromHeight(height))
	pos := int(height - start)
	err = q.Stream(ctx, func(r pack.Row) error {
		if err := r.Decode(&right); err != nil {
			return err
		}
		switch typ {
		case tezos.RightTypeBaking:
			if r, ok := right.ToBase(pos, tezos.RightTypeBaking); ok {
				resp = append(resp, r)
			}
		case tezos.RightTypeEndorsing:
			if r, ok := right.ToBase(pos, tezos.RightTypeEndorsing); ok {
				resp = append(resp, r)
			}
		default:
			if r, ok := right.ToBase(pos, tezos.RightTypeBaking); ok {
				resp = append(resp, r)
			}
			if r, ok := right.ToBase(pos, tezos.RightTypeEndorsing); ok {
				resp = append(resp, r)
			}
		}
		return nil
	})
	return resp, nil
}

func (m *Indexer) LookupAccount(ctx context.Context, addr tezos.Address) (*model.Account, error) {
	if !addr.IsValid() {
		return nil, ErrInvalidHash
	}
	table, err := m.Table(index.AccountTableKey)
	if err != nil {
		return nil, err
	}
	acc := &model.Account{}
	err = pack.NewQuery("account_by_hash", table).
		AndEqual("address", addr.Bytes22()).
		Execute(ctx, acc)
	if acc.RowId == 0 {
		err = index.ErrNoAccountEntry
	}
	if err != nil {
		return nil, err
	}
	return acc, nil
}

func (m *Indexer) LookupBaker(ctx context.Context, addr tezos.Address) (*model.Baker, error) {
	if !addr.IsValid() {
		return nil, ErrInvalidHash
	}
	table, err := m.Table(index.BakerTableKey)
	if err != nil {
		return nil, err
	}
	bkr := &model.Baker{}
	err = pack.NewQuery("baker_by_hash", table).
		AndEqual("address", addr.Bytes22()).
		Execute(ctx, bkr)
	if bkr.RowId == 0 {
		err = index.ErrNoBakerEntry
	}
	if err != nil {
		return nil, err
	}
	acc, err := m.LookupAccountId(ctx, bkr.AccountId)
	if err != nil {
		return nil, err
	}
	bkr.Account = acc
	return bkr, nil
}

func (m *Indexer) LookupContract(ctx context.Context, addr tezos.Address) (*model.Contract, error) {
	if !addr.IsValid() {
		return nil, ErrInvalidHash
	}
	table, err := m.Table(index.ContractTableKey)
	if err != nil {
		return nil, err
	}
	cc := &model.Contract{}
	err = pack.NewQuery("contract_by_hash", table).
		AndEqual("address", addr.Bytes22()).
		Execute(ctx, cc)
	if err != nil {
		return nil, err
	}
	if cc.RowId == 0 {
		return nil, index.ErrNoContractEntry
	}
	return cc, nil
}

func (m *Indexer) LookupContractId(ctx context.Context, id model.AccountID) (*model.Contract, error) {
	table, err := m.Table(index.ContractTableKey)
	if err != nil {
		return nil, err
	}
	cc := &model.Contract{}
	err = pack.NewQuery("contract_by_id", table).
		AndEqual("account_id", id).
		Execute(ctx, cc)
	if err != nil {
		return nil, err
	}
	if cc.RowId == 0 {
		return nil, index.ErrNoContractEntry
	}
	return cc, nil
}

func (m *Indexer) LookupContractType(ctx context.Context, id model.AccountID) (micheline.Type, micheline.Type, error) {
	elem, ok := m.contract_types.Get(id)
	if !ok {
		cc, err := m.LookupContractId(ctx, id)
		if err != nil {
			return micheline.Type{}, micheline.Type{}, err
		}
		elem = m.contract_types.Add(cc)
	}
	return elem.ParamType, elem.StorageType, nil
}

func (m *Indexer) LookupAccountId(ctx context.Context, id model.AccountID) (*model.Account, error) {
	table, err := m.Table(index.AccountTableKey)
	if err != nil {
		return nil, err
	}
	acc := &model.Account{}
	err = pack.NewQuery("account_by_id", table).
		AndEqual("I", id).
		Execute(ctx, acc)
	if err != nil {
		return nil, err
	}
	if acc.RowId == 0 {
		return nil, index.ErrNoAccountEntry
	}
	return acc, nil
}

func (m *Indexer) LookupBakerId(ctx context.Context, id model.AccountID) (*model.Baker, error) {
	acc, err := m.LookupAccountId(ctx, id)
	if err != nil {
		return nil, err
	}
	table, err := m.Table(index.BakerTableKey)
	if err != nil {
		return nil, err
	}
	bkr := &model.Baker{}
	err = pack.NewQuery("baker_by_id", table).
		AndEqual("account_id", id).
		Execute(ctx, bkr)
	if bkr.RowId == 0 {
		err = index.ErrNoAccountEntry
	}
	if err != nil {
		return nil, err
	}
	bkr.Account = acc
	return bkr, nil
}

func (m *Indexer) LookupAccountIds(ctx context.Context, ids []uint64) ([]*model.Account, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	table, err := m.Table(index.AccountTableKey)
	if err != nil {
		return nil, err
	}
	accs := make([]*model.Account, len(ids))
	var count int
	err = table.StreamLookup(ctx, ids, func(r pack.Row) error {
		if count >= len(accs) {
			return io.EOF
		}
		a := &model.Account{}
		if err := r.Decode(a); err != nil {
			return err
		}
		accs[count] = a
		count++
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	if count == 0 {
		return nil, index.ErrNoAccountEntry
	}
	accs = accs[:count]
	return accs, nil
}

func (m *Indexer) ListBakers(ctx context.Context, activeOnly bool) ([]*model.Baker, error) {
	bakers, err := m.Table(index.BakerTableKey)
	if err != nil {
		return nil, err
	}
	bkrs := make([]*model.Baker, 0)
	q := pack.NewQuery("list_bakers", bakers)
	if activeOnly {
		q = q.AndEqual("is_active", true)
	}
	err = q.Execute(ctx, &bkrs)
	if err != nil {
		return nil, err
	}
	bkrMap := make(map[model.AccountID]*model.Baker)
	accIds := make([]uint64, 0)
	for _, v := range bkrs {
		bkrMap[v.AccountId] = v
		accIds = append(accIds, v.AccountId.Value())
	}
	accounts, err := m.Table(index.AccountTableKey)
	if err != nil {
		return nil, err
	}
	res, err := accounts.Lookup(ctx, accIds)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	err = res.Walk(func(r pack.Row) error {
		acc := &model.Account{}
		if err := r.Decode(acc); err != nil {
			return err
		}
		bkr, ok := bkrMap[acc.RowId]
		if ok {
			bkr.Account = acc
		}
		return nil
	})
	return bkrs, err
}

func (m *Indexer) ListManaged(ctx context.Context, id model.AccountID, offset, limit uint, cursor uint64, order pack.OrderType) ([]*model.Account, error) {
	table, err := m.Table(index.AccountTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if cursor > 0 {
		offset = 0
	}
	q := pack.NewQuery("list_created_contracts", table).
		AndEqual("creator_id", id). // manager/creator id
		WithOrder(order)
	if cursor > 0 {
		if order == pack.OrderDesc {
			q = q.AndLt("I", cursor)
		} else {
			q = q.AndGt("I", cursor)
		}
	}
	accs := make([]*model.Account, 0)
	err = q.Stream(ctx, func(r pack.Row) error {
		if offset > 0 {
			offset--
			return nil
		}
		acc := &model.Account{}
		if err := r.Decode(acc); err != nil {
			return err
		}
		accs = append(accs, acc)
		if limit > 0 && len(accs) >= int(limit) {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return accs, nil
}

func (m *Indexer) LookupOp(ctx context.Context, opIdent string) ([]*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("find_tx", table)
	switch true {
	case len(opIdent) == tezos.HashTypeOperation.Base58Len() || tezos.HashTypeOperation.MatchPrefix(opIdent):
		// assume it's a hash
		oh, err := tezos.ParseOpHash(opIdent)
		if err != nil {
			return nil, ErrInvalidHash
		}
		q = q.AndEqual("hash", oh.Hash.Hash[:])
	default:
		// try parsing as event id
		eventId, err := strconv.ParseUint(opIdent, 10, 64)
		if err != nil {
			return nil, index.ErrInvalidOpID
		}
		q = q.AndEqual("height", int64(eventId>>16)).AndEqual("op_n", int64(eventId&0xFFFF))
	}
	var couldHaveStorage bool
	ops := make([]*model.Op, 0)
	err = table.Stream(ctx, q, func(r pack.Row) error {
		op := &model.Op{}
		if err := r.Decode(op); err != nil {
			return err
		}
		if op.IsSuccess && op.IsContract &&
			(op.Type == model.OpTypeTransaction || op.Type == model.OpTypeOrigination) {
			couldHaveStorage = true
		}
		ops = append(ops, op)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, index.ErrNoOpEntry
	}

	if couldHaveStorage {
		// load and merge storage updates and bigmap diffs
		for _, v := range ops {
			if !v.IsSuccess || !v.IsContract {
				continue
			}
			if v.Type != model.OpTypeTransaction && v.Type != model.OpTypeOrigination {
				continue
			}
			store, err := m.LookupStorage(ctx, v.ReceiverId, v.StorageHash, 0, v.Height)
			if err == nil {
				v.Storage = store.Storage
			}
			v.BigmapUpdates, _ = m.ListBigmapUpdates(ctx, ListRequest{OpId: v.RowId})
		}
	}
	return ops, nil
}

func (m *Indexer) LookupOpHash(ctx context.Context, opid model.OpID) tezos.OpHash {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return tezos.OpHash{}
	}
	type XOp struct {
		Hash tezos.OpHash `pack:"H"`
	}
	o := &XOp{}
	err = pack.NewQuery("find_tx", table).AndEqual("I", opid).Execute(ctx, o)
	if err != nil {
		return tezos.OpHash{}
	}
	return o.Hash
}

func (m *Indexer) LookupEndorsement(ctx context.Context, opIdent string) ([]*model.Op, error) {
	table, err := m.Table(index.EndorseOpTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("find_endorsement", table)
	switch true {
	case len(opIdent) == tezos.HashTypeOperation.Base58Len() || tezos.HashTypeOperation.MatchPrefix(opIdent):
		// assume it's a hash
		oh, err := tezos.ParseOpHash(opIdent)
		if err != nil {
			return nil, ErrInvalidHash
		}
		q = q.AndEqual("hash", oh.Hash.Hash[:])
	default:
		// try parsing as event id
		eventId, err := strconv.ParseUint(opIdent, 10, 64)
		if err != nil {
			return nil, index.ErrInvalidOpID
		}
		q = q.AndEqual("height", int64(eventId>>16)).AndEqual("op_n", int64(eventId&0xFFFF))
	}
	ops := make([]*model.Op, 0)
	err = table.Stream(ctx, q, func(r pack.Row) error {
		ed := &model.Endorsement{}
		if err := r.Decode(ed); err != nil {
			return err
		}
		ops = append(ops, ed.ToOp())
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, index.ErrNoOpEntry
	}
	return ops, nil
}

func (m *Indexer) FindActivatedAccount(ctx context.Context, addr tezos.Address) (*model.Account, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	type Xop struct {
		SenderId  model.AccountID `pack:"S"`
		CreatorId model.AccountID `pack:"M"`
		Data      string          `pack:"a"`
	}
	var o Xop
	err = pack.NewQuery("find_activation", table).
		WithFields("sender_id", "creator_id", "data").
		WithoutCache().
		AndEqual("type", model.OpTypeActivation).
		Stream(ctx, func(r pack.Row) error {
			if err := r.Decode(&o); err != nil {
				return err
			}
			// data contains hex(secret),blinded_address
			data := strings.Split(o.Data, ",")
			if len(data) != 2 {
				// skip broken records
				return nil
			}
			ba, err := tezos.DecodeBlindedAddress(data[1])
			if err != nil {
				// skip broken records
				return nil
			}
			if addr.Equal(ba) {
				return io.EOF // found
			}
			return nil
		})
	if err != io.EOF {
		if err == nil {
			err = index.ErrNoAccountEntry
		}
		return nil, err
	}
	// lookup account by id
	if o.CreatorId != 0 {
		return m.LookupAccountId(ctx, o.CreatorId)
	}
	return m.LookupAccountId(ctx, o.SenderId)
}

func (m *Indexer) FindLatestDelegation(ctx context.Context, id model.AccountID, height int64) (*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	o := &model.Op{}
	err = pack.NewQuery("find_last_delegation", table).
		WithoutCache().
		WithDesc().
		WithLimit(1).
		AndEqual("type", model.OpTypeDelegation). // type
		AndEqual("sender_id", id).                // search for sender account id
		AndNotEqual("baker_id", 0).               // delegate id
		AndLt("height", height).                  // must be in a previous block
		Execute(ctx, o)
	if err != nil {
		return nil, err
	}
	if o.RowId == 0 {
		return nil, index.ErrNoOpEntry
	}
	return o, nil
}

func (m *Indexer) FindOrigination(ctx context.Context, id model.AccountID, height int64) (*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	o := &model.Op{}
	err = pack.NewQuery("find_origination", table).
		WithoutCache().
		WithDesc().
		WithLimit(1).
		AndGte("height", height).                  // first seen height
		AndEqual("type", model.OpTypeOrigination). // type
		AndEqual("receiver_id", id).               // search for receiver account id
		Execute(ctx, o)
	if err != nil {
		return nil, err
	}
	if o.RowId == 0 {
		return nil, index.ErrNoOpEntry
	}
	return o, nil
}

func (m *Indexer) LookupOpIds(ctx context.Context, ids []uint64) ([]*model.Op, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	ops := make([]*model.Op, len(ids))
	var count int
	err = table.StreamLookup(ctx, ids, func(r pack.Row) error {
		if count >= len(ops) {
			return io.EOF
		}
		op := &model.Op{}
		if err := r.Decode(op); err != nil {
			return err
		}
		ops[count] = op
		count++
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	if count == 0 {
		return nil, index.ErrNoOpEntry
	}
	ops = ops[:count]
	return ops, nil
}

// Note: offset and limit count in atomar operations
func (m *Indexer) ListBlockOps(ctx context.Context, r ListRequest) ([]*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}
	q := pack.NewQuery("list_block_ops", table).
		WithOrder(r.Order).
		AndEqual("height", r.Since).
		WithLimit(int(r.Limit)).
		WithOffset(int(r.Offset))

	if r.SenderId > 0 {
		q = q.AndEqual("sender_id", r.SenderId)
	}
	if r.ReceiverId > 0 {
		q = q.AndEqual("receiver_id", r.ReceiverId)
	}
	if r.Account != nil {
		q = q.Or(
			pack.Equal("sender_id", r.Account.RowId),
			pack.Equal("receiver_id", r.Account.RowId),
			pack.Equal("baker_id", r.Account.RowId),
			pack.Equal("creator_id", r.Account.RowId),
		)
	}
	if r.Cursor > 0 {
		opn := int64(r.Cursor & 0xFFFF)
		if r.Order == pack.OrderDesc {
			q = q.AndLt("op_n", opn)
		} else {
			q = q.AndGt("op_n", opn)
		}
	}
	if len(r.Typs) > 0 && r.Mode.IsValid() {
		if r.Mode.IsScalar() {
			q = q.AndCondition("type", r.Mode, r.Typs[0])
		} else {
			q = q.AndCondition("type", r.Mode, r.Typs)
		}
	}
	ops := make([]*model.Op, 0, r.Limit)
	if err = q.Execute(ctx, &ops); err != nil {
		return nil, err
	}
	return ops, nil
}

func (m *Indexer) ListBlockEndorsements(ctx context.Context, r ListRequest) ([]*model.Endorsement, error) {
	table, err := m.Table(index.EndorseOpTableKey)
	if err != nil {
		return nil, err
	}

	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}

	q := pack.NewQuery("list_block_endorse", table).
		WithOrder(r.Order).
		AndEqual("height", r.Since).
		WithLimit(int(r.Limit)).
		WithOffset(int(r.Offset))

	if r.Cursor > 0 {
		opn := int64(r.Cursor & 0xFFFF)
		if r.Order == pack.OrderDesc {
			q = q.AndLt("op_n", opn)
		} else {
			q = q.AndGt("op_n", opn)
		}
	}
	if r.SenderId > 0 {
		q = q.AndEqual("sender_id", r.SenderId)
	}
	if r.Account != nil {
		q = q.AndEqual("sender_id", r.Account.RowId)
	}
	endorse := make([]*model.Endorsement, 0)
	err = q.Execute(ctx, &endorse)
	if err != nil {
		return nil, err
	}
	return endorse, nil
}

// Note:
// - order is defined by funding or spending operation
// - offset and limit counts in atomar ops
// - high traffic addresses may have many, so we use query limits
func (m *Indexer) ListAccountOps(ctx context.Context, r ListRequest) ([]*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}

	// clamp time range to account lifetime
	r.Since = util.Max64(r.Since, r.Account.FirstSeen-1)
	r.Until = util.NonZeroMin64(r.Until, r.Account.LastSeen)

	// check if we should list delegations, consider different query modes
	withDelegation := r.WithDelegation()
	onlyDelegation := withDelegation && len(r.Typs) == 1

	// list all ops where this address is any of
	// - sender
	// - receiver
	// - delegate (only for delegation type)
	q := pack.NewQuery("list_account_ops", table).
		WithOrder(r.Order).
		WithLimit(int(r.Limit)).
		WithOffset(int(r.Offset))

	switch {
	case r.SenderId > 0: // anything received by us from this sender
		if onlyDelegation {
			q = q.Or(
				// regular delegation is del + delegate set
				// internal delegation are tx + delegate set
				// regular origination + delegation is orig + delegate set
				// internal origination + delegation is orig + delegate set
				pack.And(
					pack.In("type", []model.OpType{model.OpTypeDelegation, model.OpTypeOrigination}),
					pack.Equal("baker_id", r.Account.RowId),
				),
				// regular un/re-delegation is del + receiver set
				// internal un/re-delegation is del + receiver set
				pack.And(
					pack.Equal("type", model.OpTypeDelegation),
					pack.Equal("receiver_id", r.Account.RowId),
				),
			)
			r.Typs = nil
		} else if withDelegation {
			q = q.Or(
				pack.Equal("receiver_id", r.Account.RowId),
				pack.Equal("baker_id", r.Account.RowId),
			)
		} else {
			q = q.AndEqual("receiver_id", r.Account.RowId)
		}
		q = q.AndEqual("sender_id", r.SenderId)
	case r.ReceiverId > 0: // anything sent by us to this receiver
		if onlyDelegation {
			q = q.Or(
				// regular delegation is del + delegate set
				// internal delegation are tx + delegate set
				// regular origination + delegation is orig + delegate set
				// internal origination + delegation is orig + delegate set
				pack.And(
					pack.In("type", []model.OpType{model.OpTypeDelegation, model.OpTypeOrigination}),
					pack.Equal("baker_id", r.ReceiverId),
				),
				// regular un/re-delegation is del + receiver set
				// internal un/re-delegation is del + receiver set
				pack.And(
					pack.Equal("type", model.OpTypeDelegation),
					pack.Equal("receiver_id", r.ReceiverId),
				),
			)
			r.Typs = nil
		} else if withDelegation {
			q = q.Or(
				pack.Equal("receiver_id", r.ReceiverId),
				pack.Equal("baker_id", r.ReceiverId),
			)
		} else {
			q = q.AndEqual("receiver_id", r.ReceiverId)
		}
		q = q.AndEqual("sender_id", r.Account.RowId)
	default: // anything sent or received by us
		if withDelegation {
			q = q.Or(
				pack.Equal("sender_id", r.Account.RowId),
				pack.Equal("receiver_id", r.Account.RowId),
				pack.Equal("baker_id", r.Account.RowId),
			)
		} else {
			q = q.Or(
				pack.Equal("sender_id", r.Account.RowId),
				pack.Equal("receiver_id", r.Account.RowId),
			)
		}
	}

	if r.Cursor > 0 {
		height := int64(r.Cursor >> 16)
		opn := int64(r.Cursor & 0xFFFF)
		if r.Order == pack.OrderDesc {
			q = q.Or(
				pack.Lt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Lt("op_n", opn),
				),
			)
		} else {
			q = q.Or(
				pack.Gt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Gt("op_n", opn),
				),
			)
		}
	}

	if r.Since > 0 || r.Account.FirstSeen > 0 {
		q = q.AndGt("height", util.Max64(r.Since, r.Account.FirstSeen-1))
	}
	if r.Until > 0 || r.Account.LastSeen > 0 {
		q = q.AndLte("height", util.NonZeroMin64(r.Until, r.Account.LastSeen))
	}
	if len(r.Typs) > 0 && r.Mode.IsValid() {
		if r.Mode.IsScalar() {
			q = q.AndCondition("type", r.Mode, r.Typs[0])
		} else {
			q = q.AndCondition("type", r.Mode, r.Typs)
		}
	}

	ops := make([]*model.Op, 0)
	if err := q.Execute(ctx, &ops); err != nil {
		return nil, err
	}

	if r.WithStorage {
		// load and merge storage updates and bigmap diffs
		for _, v := range ops {
			if !v.IsSuccess || !v.IsContract {
				continue
			}
			if v.Type != model.OpTypeTransaction && v.Type != model.OpTypeOrigination {
				continue
			}
			store, err := m.LookupStorage(ctx, v.ReceiverId, v.StorageHash, 0, v.Height)
			if err == nil {
				v.Storage = store.Storage
			}
			v.BigmapUpdates, _ = m.ListBigmapUpdates(ctx, ListRequest{OpId: v.RowId})
		}
	}

	return ops, nil
}

// Note:
// - order is defined by funding or spending operation
// - offset and limit counts in collapsed ops (all batch/internal contents)
// - high traffic addresses may have many, so we use query limits
func (m *Indexer) ListAccountOpsCollapsed(ctx context.Context, r ListRequest) ([]*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}

	// clamp time range to account lifetime
	r.Since = util.Max64(r.Since, r.Account.FirstSeen-1)
	r.Until = util.NonZeroMin64(r.Until, r.Account.LastSeen)

	// check if we should list delegations, consider different query modes
	withDelegation := r.WithDelegation()
	onlyDelegation := withDelegation && len(r.Typs) == 1

	// list all ops where this address is any of
	// - sender
	// - receiver
	// - delegate
	q := pack.NewQuery("list_account_ops", table).
		WithOrder(r.Order)

	switch {
	case r.SenderId > 0:
		if onlyDelegation {
			q = q.Or(
				// regular delegation is del + delegate set
				// internal delegation are tx + delegate set
				// regular origination + delegation is orig + delegate set
				// internal origination + delegation is orig + delegate set
				pack.And(
					pack.In("type", []model.OpType{model.OpTypeDelegation, model.OpTypeOrigination}),
					pack.Equal("baker_id", r.Account.RowId),
				),
				// regular un/re-delegation is del + receiver set
				// internal un/re-delegation is del + receiver set
				pack.And(
					pack.Equal("type", model.OpTypeDelegation),
					pack.Equal("receiver_id", r.Account.RowId),
				),
			)
			r.Typs = nil
		} else if withDelegation {
			q = q.Or(
				pack.Equal("receiver_id", r.Account.RowId),
				pack.Equal("baker_id", r.Account.RowId),
			)
		} else {
			q = q.AndEqual("receiver_id", r.Account.RowId)
		}
		q = q.AndEqual("sender_id", r.SenderId)

	case r.ReceiverId > 0:
		if onlyDelegation {
			q = q.Or(
				// regular delegation is del + delegate set
				// internal delegation are tx + delegate set
				// regular origination + delegation is orig + delegate set
				// internal origination + delegation is orig + delegate set
				pack.And(
					pack.In("type", []model.OpType{model.OpTypeDelegation, model.OpTypeOrigination}),
					pack.Equal("baker_id", r.ReceiverId),
				),
				// regular un/re-delegation is del + receiver set
				// internal un/re-delegation is del + receiver set
				pack.And(
					pack.Equal("type", model.OpTypeDelegation),
					pack.Equal("receiver_id", r.ReceiverId),
				),
			)
			r.Typs = nil
		} else if withDelegation {
			q = q.Or(
				pack.Equal("receiver_id", r.ReceiverId),
				pack.Equal("baker_id", r.ReceiverId),
			)
		} else {
			q = q.AndEqual("receiver_id", r.ReceiverId)
		}
		q = q.AndEqual("sender_id", r.Account.RowId)
	default:
		if withDelegation {
			q = q.Or(
				pack.Equal("sender_id", r.Account.RowId),
				pack.Equal("receiver_id", r.Account.RowId),
				pack.Equal("baker_id", r.Account.RowId),
			)
		} else {
			q = q.Or(
				pack.Equal("sender_id", r.Account.RowId),
				pack.Equal("receiver_id", r.Account.RowId),
			)
		}
	}

	// FIXME:
	// - if S/R is only in one internal op, pull the entire op group
	ops := make([]*model.Op, 0)

	if r.Cursor > 0 {
		height := int64(r.Cursor >> 16)
		opn := int64(r.Cursor & 0xFFFF)
		if r.Order == pack.OrderDesc {
			q = q.Or(
				pack.Lt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Lt("op_n", opn),
				),
			)
		} else {
			q = q.Or(
				pack.Gt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Gt("op_n", opn),
				),
			)
		}
	}

	if r.Since > 0 || r.Account.FirstSeen > 0 {
		q = q.AndGt("height", util.Max64(r.Since, r.Account.FirstSeen-1))
	}
	if r.Until > 0 || r.Account.LastSeen > 0 {
		q = q.AndLte("height", util.NonZeroMin64(r.Until, r.Account.LastSeen))
	}
	if len(r.Typs) > 0 && r.Mode.IsValid() {
		if r.Mode.IsScalar() {
			q = q.AndCondition("type", r.Mode, r.Typs[0])
		} else {
			q = q.AndCondition("type", r.Mode, r.Typs)
		}
	}
	var (
		lastP      int = -1
		lastHeight int64
		count      int
	)
	err = q.Stream(ctx, func(rx pack.Row) error {
		op := model.AllocOp()
		if err := rx.Decode(op); err != nil {
			return err
		}
		// detect next op group (works in both directions)
		isFirst := lastP < 0
		isNext := op.OpP != lastP || op.Height != lastHeight
		lastP, lastHeight = op.OpP, op.Height

		// skip offset groups
		if r.Offset > 0 {
			if isNext && !isFirst {
				r.Offset--
			} else {
				return nil
			}
			if r.Offset > 0 {
				return nil
			}
		}

		// stop at first result after group end
		if isNext && r.Limit > 0 && count == int(r.Limit) {
			return io.EOF
		}

		ops = append(ops, op)

		// count op groups
		if isNext {
			count++
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return ops, nil
}

func (m *Indexer) ListBakerEndorsements(ctx context.Context, r ListRequest) ([]*model.Op, error) {
	table, err := m.Table(index.EndorseOpTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}

	// clamp time range to account lifetime
	r.Since = util.Max64(r.Since, r.Account.FirstSeen-1)
	r.Until = util.NonZeroMin64(r.Until, r.Account.LastSeen)

	q := pack.NewQuery("list_baker_endorsements", table).
		WithOrder(r.Order).
		WithLimit(int(r.Limit)).
		WithOffset(int(r.Offset)).
		AndEqual("sender_id", r.Account.RowId)

	if r.Cursor > 0 {
		height := int64(r.Cursor >> 16)
		opn := int64(r.Cursor & 0xFFFF)
		if r.Order == pack.OrderDesc {
			q = q.Or(
				pack.Lt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Lt("op_n", opn),
				),
			)
		} else {
			q = q.Or(
				pack.Gt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Gt("op_n", opn),
				),
			)
		}
	}

	if r.Since > 0 || r.Account.FirstSeen > 0 {
		q = q.AndGt("height", util.Max64(r.Since, r.Account.FirstSeen-1))
	}
	if r.Until > 0 || r.Account.LastSeen > 0 {
		q = q.AndLte("height", util.NonZeroMin64(r.Until, r.Account.LastSeen))
	}

	ops := make([]*model.Op, 0)
	var end model.Endorsement
	if err := q.Stream(ctx, func(row pack.Row) error {
		if err := row.Decode(&end); err != nil {
			return err
		}
		ops = append(ops, end.ToOp())
		return nil
	}); err != nil {
		return nil, err
	}
	return ops, nil
}

func (m *Indexer) ListContractCalls(ctx context.Context, r ListRequest) ([]*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}

	// clamp time range to account lifetime
	r.Since = util.Max64(r.Since, r.Account.FirstSeen-1)
	r.Until = util.NonZeroMin64(r.Until, r.Account.LastSeen)

	// list all successful tx (calls) received by this contract
	q := pack.NewQuery("list_calls_recv", table).
		WithOrder(r.Order).
		WithLimit(int(r.Limit)).
		WithOffset(int(r.Offset)).
		AndEqual("receiver_id", r.Account.RowId).
		AndEqual("is_success", true)

	if r.Account.Address.IsContract() {
		q = q.AndEqual("type", model.OpTypeTransaction)
	} else if r.Account.Address.IsRollup() {
		q = q.AndIn("type", []model.OpType{
			model.OpTypeTransaction,
			model.OpTypeRollupOrigination,
			model.OpTypeRollupTransaction,
		})
	}

	if r.SenderId > 0 {
		q = q.AndEqual("sender_id", r.SenderId)
	}

	// add entrypoint filter
	switch len(r.Entrypoints) {
	case 0:
		// none, search op type
	case 1:
		// any single
		q = q.AndCondition("entrypoint_id", r.Mode, r.Entrypoints[0]) // entrypoint_id
	default:
		// in/nin
		q = q.AndCondition("entrypoint_id", r.Mode, r.Entrypoints) // entrypoint_ids
	}

	if r.Cursor > 0 {
		height := int64(r.Cursor >> 16)
		opn := int64(r.Cursor & 0xFFFF)
		if r.Order == pack.OrderDesc {
			q = q.Or(
				pack.Lt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Lt("op_n", opn),
				),
			)
		} else {
			q = q.Or(
				pack.Gt("height", height),
				pack.And(
					pack.Equal("height", height),
					pack.Gt("op_n", opn),
				),
			)
		}
	}

	if r.Since > 0 {
		q = q.AndGt("height", r.Since)
	}
	if r.Until > 0 {
		q = q.AndLte("height", r.Until)
	}
	ops := make([]*model.Op, 0, util.NonZero(int(r.Limit), 512))
	if err := q.Execute(ctx, &ops); err != nil {
		return nil, err
	}

	if r.WithStorage {
		// load and merge storage updates and bigmap diffs
		for _, v := range ops {
			if !v.IsSuccess || !v.IsContract {
				continue
			}
			if v.Type != model.OpTypeTransaction && v.Type != model.OpTypeOrigination {
				continue
			}
			store, err := m.LookupStorage(ctx, v.ReceiverId, v.StorageHash, 0, v.Height)
			if err == nil {
				v.Storage = store.Storage
			}
			v.BigmapUpdates, _ = m.ListBigmapUpdates(ctx, ListRequest{OpId: v.RowId})
		}
	}

	return ops, nil
}

func (m *Indexer) FindLastCall(ctx context.Context, acc model.AccountID, from, to int64) (*model.Op, error) {
	table, err := m.Table(index.OpTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("last_call", table).
		WithDesc().
		WithLimit(1)
	if from > 0 {
		q = q.AndGt("height", from)
	}
	if to > 0 {
		q = q.AndLte("height", to)
	}
	op := &model.Op{}
	err = q.AndEqual("receiver_id", acc).
		AndEqual("type", model.OpTypeTransaction).
		AndEqual("is_contract", true).
		AndEqual("is_success", true).
		Execute(ctx, op)
	if err != nil {
		return nil, err
	}
	if op.RowId == 0 {
		return nil, index.ErrNoOpEntry
	}
	return op, nil
}

func (m *Indexer) ElectionByHeight(ctx context.Context, height int64) (*model.Election, error) {
	table, err := m.Table(index.ElectionTableKey)
	if err != nil {
		return nil, err
	}
	// we are looking for the last election with start_height <= height
	e := &model.Election{}
	err = pack.NewQuery("election_height", table).
		WithDesc().
		WithLimit(1).
		AndLte("start_height", height).
		Execute(ctx, e)
	if err != nil {
		return nil, err
	}
	if e.RowId == 0 {
		return nil, index.ErrNoElectionEntry
	}
	return e, nil
}

func (m *Indexer) ElectionById(ctx context.Context, id model.ElectionID) (*model.Election, error) {
	table, err := m.Table(index.ElectionTableKey)
	if err != nil {
		return nil, err
	}
	e := &model.Election{}
	err = pack.NewQuery("election_id", table).
		WithLimit(1).
		AndEqual("I", id).
		Execute(ctx, e)
	if err != nil {
		return nil, err
	}
	if e.RowId == 0 {
		return nil, index.ErrNoElectionEntry
	}
	return e, nil
}

func (m *Indexer) VotesByElection(ctx context.Context, id model.ElectionID) ([]*model.Vote, error) {
	table, err := m.Table(index.VoteTableKey)
	if err != nil {
		return nil, err
	}
	votes := make([]*model.Vote, 0)
	err = pack.NewQuery("list_votes", table).
		AndEqual("election_id", id).
		Execute(ctx, &votes)
	if err != nil {
		return nil, err
	}
	if len(votes) == 0 {
		return nil, index.ErrNoVoteEntry
	}
	return votes, nil
}

// r.Since is the true vote start block
func (m *Indexer) ListVoters(ctx context.Context, r ListRequest) ([]*model.Voter, error) {
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}

	// Step 1
	// collect voters from governance roll snapshot
	rollsTable, err := m.Table(index.RollsTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("list_voters", rollsTable).
		WithLimit(int(r.Limit)).
		WithOffset(int(r.Offset)).
		AndEqual("height", r.Since-1) // snapshots are made at end of previous vote

	if r.Cursor > 0 {
		if r.Order == pack.OrderDesc {
			q = q.AndLt("I", r.Cursor)
		} else {
			q = q.AndGt("I", r.Cursor)
		}
	}
	voters := make(map[model.AccountID]*model.Voter)
	snap := &model.RollSnapshot{}
	err = q.Stream(ctx, func(r pack.Row) error {
		if err := r.Decode(snap); err != nil {
			return err
		}
		voters[snap.AccountId] = &model.Voter{
			RowId: snap.AccountId,
			Rolls: snap.Rolls,
			Stake: snap.Stake,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Step 2: list ballots
	ballotTable, err := m.Table(index.BallotTableKey)
	if err != nil {
		return nil, err
	}
	ballot := &model.Ballot{}
	err = pack.NewQuery("list_voters", ballotTable).
		AndEqual("voting_period", r.Period).
		Stream(ctx, func(r pack.Row) error {
			if err := r.Decode(ballot); err != nil {
				return err
			}
			if voter, ok := voters[ballot.SourceId]; ok {
				voter.Ballot = ballot.Ballot
				voter.Time = ballot.Time
				voter.HasVoted = true
				found := false
				for _, v := range voter.Proposals {
					if v != ballot.ProposalId {
						continue
					}
					found = true
					break
				}
				if !found {
					voter.Proposals = append(voter.Proposals, ballot.ProposalId)
				}
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	out := make([]*model.Voter, 0, len(voters))
	for _, v := range voters {
		out = append(out, v)
	}
	if r.Order == pack.OrderAsc {
		sort.Slice(out, func(i, j int) bool { return out[i].RowId < out[j].RowId })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].RowId > out[j].RowId })
	}
	return out, nil
}

func (m *Indexer) ProposalsByElection(ctx context.Context, id model.ElectionID) ([]*model.Proposal, error) {
	table, err := m.Table(index.ProposalTableKey)
	if err != nil {
		return nil, err
	}
	proposals := make([]*model.Proposal, 0)
	err = pack.NewQuery("list_proposals", table).
		AndEqual("election_id", id).
		Execute(ctx, &proposals)
	if err != nil {
		return nil, err
	}
	return proposals, nil
}

func (m *Indexer) LookupProposal(ctx context.Context, proto tezos.ProtocolHash) (*model.Proposal, error) {
	if !proto.IsValid() {
		return nil, ErrInvalidHash
	}

	table, err := m.Table(index.ProposalTableKey)
	if err != nil {
		return nil, err
	}

	// use hash and type to protect against duplicates
	prop := &model.Proposal{}
	err = pack.NewQuery("proposal_by_hash", table).
		AndEqual("hash", proto.Hash.Hash).
		Execute(ctx, prop)
	if err != nil {
		return nil, err
	}
	if prop.RowId == 0 {
		return nil, index.ErrNoProposalEntry
	}
	return prop, nil
}

func (m *Indexer) LookupProposalIds(ctx context.Context, ids []uint64) ([]*model.Proposal, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	table, err := m.Table(index.ProposalTableKey)
	if err != nil {
		return nil, err
	}
	props := make([]*model.Proposal, len(ids))
	var count int
	err = table.StreamLookup(ctx, ids, func(r pack.Row) error {
		if count >= len(props) {
			return io.EOF
		}
		p := &model.Proposal{}
		if err := r.Decode(p); err != nil {
			return err
		}
		props[count] = p
		count++
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	if count == 0 {
		return nil, index.ErrNoProposalEntry
	}
	props = props[:count]
	return props, nil
}

func (m *Indexer) ListBallots(ctx context.Context, r ListRequest) ([]*model.Ballot, error) {
	table, err := m.Table(index.BallotTableKey)
	if err != nil {
		return nil, err
	}
	// cursor and offset are mutually exclusive
	if r.Cursor > 0 {
		r.Offset = 0
	}
	q := pack.NewQuery("list_ballots", table).
		WithOrder(r.Order).
		WithOffset(int(r.Offset)).
		WithLimit(int(r.Limit))
	if r.Account != nil {
		// clamp time range to account lifetime
		r.Since = util.Max64(r.Since, r.Account.FirstSeen-1)
		r.Until = util.NonZeroMin64(r.Until, r.Account.LastSeen)
		q = q.AndEqual("source_id", r.Account.RowId)
	}
	if r.Period > 0 {
		q = q.AndEqual("voting_period", r.Period)
	}
	if r.Since > 0 {
		q = q.AndGt("height", r.Since)
	}
	if r.Until > 0 {
		q = q.AndLte("height", r.Until)
	}
	if r.Cursor > 0 {
		if r.Order == pack.OrderDesc {
			q = q.AndLt("I", r.Cursor)
		} else {
			q = q.AndGt("I", r.Cursor)
		}
	}
	ballots := make([]*model.Ballot, 0)
	if err := q.Execute(ctx, &ballots); err != nil {
		return nil, err
	}
	return ballots, nil
}

func (m *Indexer) ListContractBigmaps(ctx context.Context, acc model.AccountID, height int64) ([]*model.BigmapAlloc, error) {
	table, err := m.Table(index.BigmapAllocTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("list_bigmaps", table).AndEqual("account_id", acc)
	if height > 0 {
		// bigmap must exist at this height
		q = q.AndLte("alloc_height", height).
			// and not be deleted
			Or(
				pack.Equal("delete_height", 0),
				pack.Gt("delete_height", height),
			)
	}
	allocs := make([]*model.BigmapAlloc, 0)
	err = q.Stream(ctx, func(r pack.Row) error {
		a := &model.BigmapAlloc{}
		if err := r.Decode(a); err != nil {
			return err
		}
		allocs = append(allocs, a)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(allocs, func(i, j int) bool { return allocs[i].BigmapId < allocs[j].BigmapId })
	return allocs, nil
}

func (m *Indexer) LookupBigmapAlloc(ctx context.Context, id int64) (*model.BigmapAlloc, error) {
	table, err := m.Table(index.BigmapAllocTableKey)
	if err != nil {
		return nil, err
	}
	alloc := &model.BigmapAlloc{}
	err = pack.NewQuery("search_bigmap", table).
		AndEqual("bigmap_id", id).
		Execute(ctx, alloc)
	if err != nil {
		return nil, err
	}
	if alloc.RowId == 0 {
		return nil, index.ErrNoBigmapAlloc
	}
	return alloc, nil
}

// only type info is relevant
func (m *Indexer) LookupBigmapType(ctx context.Context, id int64) (*model.BigmapAlloc, error) {
	alloc, ok := m.bigmap_types.GetType(id)
	if ok {
		return alloc, nil
	}
	table, err := m.Table(index.BigmapAllocTableKey)
	if err != nil {
		return nil, err
	}
	alloc = &model.BigmapAlloc{}
	err = pack.NewQuery("search_bigmap", table).
		AndEqual("bigmap_id", id).
		Execute(ctx, alloc)
	if err != nil {
		return nil, err
	}
	if alloc.RowId == 0 {
		return nil, index.ErrNoBigmapAlloc
	}
	m.bigmap_types.Add(alloc)
	return alloc, nil
}

func (m *Indexer) ListHistoricBigmapKeys(ctx context.Context, r ListRequest) ([]*model.BigmapKV, error) {
	hist, ok := m.bigmap_values.Get(r.BigmapId, r.Since)
	if !ok {
		start := time.Now()
		table, err := m.Table(index.BigmapUpdateTableKey)
		if err != nil {
			return nil, err
		}

		// check if we have any previous bigmap state cached
		prev, ok := m.bigmap_values.GetBest(r.BigmapId, r.Since)
		if ok {
			// update from existing cache
			hist, err = m.bigmap_values.Update(ctx, prev, table, r.Since)
			if err != nil {
				return nil, err
			}
			log.Debugf("Updated history cache for bigmap %d from height %d to height %d with %d entries in %s",
				r.BigmapId, prev.Height, r.Since, hist.Len(), time.Since(start))

		} else {
			// build a new cache
			hist, err = m.bigmap_values.Build(ctx, table, r.BigmapId, r.Since)
			if err != nil {
				return nil, err
			}
			log.Debugf("Built history cache for bigmap %d at height %d with %d entries in %s",
				r.BigmapId, r.Since, hist.Len(), time.Since(start))
		}
	}

	// cursor and offset are mutually exclusive, we use offset below
	// Note that cursor starts at 1
	if r.Cursor > 0 {
		r.Offset = uint(r.Cursor - 1)
	}
	var from, to int
	if r.Order == pack.OrderAsc {
		from, to = int(r.Offset), int(r.Offset+r.Limit)
	} else {
		l := hist.Len()
		from = util.Max(0, l-int(r.Offset-r.Limit))
		to = from + int(r.Limit)
	}

	// get from cache
	var items []*model.BigmapKV
	if r.BigmapKey.IsValid() {
		if item := hist.Get(r.BigmapKey); item != nil {
			items = []*model.BigmapKV{item}
		}
	} else {
		items = hist.Range(from, to)
	}

	// maybe reverse order
	if r.Order == pack.OrderDesc {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	return items, nil
}

func (m *Indexer) ListBigmapKeys(ctx context.Context, r ListRequest) ([]*model.BigmapKV, error) {
	table, err := m.Table(index.BigmapValueTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("list_bigmap", table).
		WithOrder(r.Order).
		AndEqual("bigmap_id", r.BigmapId)
	if r.Cursor > 0 {
		r.Offset = 0
		if r.Order == pack.OrderDesc {
			q = q.AndLt("I", r.Cursor)
		} else {
			q = q.AndGt("I", r.Cursor)
		}
	}
	if r.BigmapKey.IsValid() {
		// assume hash collisions
		q = q.WithDesc().AndEqual("key_id", model.GetKeyId(r.BigmapId, r.BigmapKey))
	}
	items := make([]*model.BigmapKV, 0)
	err = q.Stream(ctx, func(row pack.Row) error {
		// skip before decoding
		if r.Offset > 0 {
			r.Offset--
			return nil
		}
		b := &model.BigmapKV{}
		if err := row.Decode(b); err != nil {
			return err
		}

		// skip hash collisions on key_id
		if r.BigmapKey.IsValid() && !r.BigmapKey.Equal(b.GetKeyHash()) {
			// log.Infof("Skip hash collision for item %s %d %d key %x", b.Action, b.BigmapId, b.RowId, b.Key)
			return nil
		}

		// log.Infof("Found item %s %d %d key %x", b.Action, b.BigmapId, b.RowId, b.Key)
		items = append(items, b)
		if len(items) == int(r.Limit) {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return items, nil
}

func (m *Indexer) ListBigmapUpdates(ctx context.Context, r ListRequest) ([]model.BigmapUpdate, error) {
	table, err := m.Table(index.BigmapUpdateTableKey)
	if err != nil {
		return nil, err
	}
	q := pack.NewQuery("list_bigmap", table).WithOrder(r.Order)
	if r.BigmapId > 0 {
		q = q.AndEqual("bigmap_id", r.BigmapId)
	}
	if r.OpId > 0 {
		q = q.AndEqual("op_id", r.OpId)
	}
	if r.Cursor > 0 {
		r.Offset = 0
		if r.Order == pack.OrderDesc {
			q = q.AndLt("I", r.Cursor)
		} else {
			q = q.AndGt("I", r.Cursor)
		}
	}
	if r.Since > 0 {
		q = q.AndGte("height", r.Since)
	}
	if r.Until > 0 {
		q = q.AndLte("height", r.Until)
	}
	if r.BigmapKey.IsValid() {
		q = q.AndEqual("key_id", model.GetKeyId(r.BigmapId, r.BigmapKey))
	}
	items := make([]model.BigmapUpdate, 0)
	err = table.Stream(ctx, q, func(row pack.Row) error {
		if r.Offset > 0 {
			r.Offset--
			return nil
		}
		b := model.BigmapUpdate{}
		if err := row.Decode(&b); err != nil {
			return err
		}
		// skip hash collisions on key_id
		if r.BigmapKey.IsValid() && !r.BigmapKey.Equal(b.GetKeyHash()) {
			return nil
		}
		items = append(items, b)
		if len(items) == int(r.Limit) {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return items, nil
}

// luck, performance, contribution (reliability)
func (m *Indexer) BakerPerformance(ctx context.Context, id model.AccountID, fromCycle, toCycle int64) ([3]int64, error) {
	perf := [3]int64{}
	table, err := m.Table(index.IncomeTableKey)
	if err != nil {
		return perf, err
	}
	q := pack.NewQuery("baker_income", table).
		AndEqual("account_id", id).
		AndRange("cycle", fromCycle, toCycle)
	var count int64
	income := &model.Income{}
	err = q.Stream(ctx, func(r pack.Row) error {
		if err := r.Decode(income); err != nil {
			return err
		}
		perf[0] += income.LuckPct
		perf[1] += income.PerformancePct
		perf[2] += income.ContributionPct
		count++
		return nil
	})
	if err != nil {
		return perf, err
	}
	if count > 0 {
		perf[0] /= count
		perf[1] /= count
		perf[2] /= count
	}
	return perf, nil
}

func (m *Indexer) UpdateMetadata(ctx context.Context, md *model.Metadata) error {
	if md == nil {
		return nil
	}
	table, err := m.Table(index.MetadataTableKey)
	if err != nil {
		return err
	}
	if md.RowId > 0 {
		return table.Update(ctx, md)
	}
	err = pack.NewQuery("api.metadata.search", table).
		WithoutCache().
		WithLimit(1).
		AndEqual("address", md.Address.Bytes22()).
		AndEqual("asset_id", md.AssetId).
		Stream(ctx, func(r pack.Row) error {
			m := &model.Metadata{}
			if err := r.Decode(m); err != nil {
				return err
			}
			md.RowId = m.RowId
			return nil
		})
	if err != nil {
		return err
	}
	return table.Update(ctx, md)
}

func (m *Indexer) RemoveMetadata(ctx context.Context, md *model.Metadata) error {
	if md == nil {
		return nil
	}
	table, err := m.Table(index.MetadataTableKey)
	if err != nil {
		return err
	}
	if md.RowId > 0 {
		return table.DeleteIds(ctx, []uint64{md.RowId})
	}
	q := pack.NewQuery("api.metadata.delete", table).
		AndEqual("address", md.Address.Bytes22()).
		AndEqual("asset_id", md.AssetId).
		AndEqual("is_asset", md.IsAsset)
	_, err = table.Delete(ctx, q)
	return err
}

func (m *Indexer) PurgeMetadata(ctx context.Context) error {
	table, err := m.Table(index.MetadataTableKey)
	if err != nil {
		return err
	}
	q := pack.NewQuery("api.metadata.purge", table).AndGte("row_id", 0)
	if _, err := table.Delete(ctx, q); err != nil {
		return err
	}
	return table.Flush(ctx)
}

func (m *Indexer) UpsertMetadata(ctx context.Context, entries []*model.Metadata) error {
	table, err := m.Table(index.MetadataTableKey)
	if err != nil {
		return err
	}

	// copy slice ptrs
	match := make([]*model.Metadata, len(entries))
	copy(match, entries)

	// find existing metadata entries for update
	upd := make([]pack.Item, 0)
	md := &model.Metadata{}
	err = pack.NewQuery("api.metadata.upsert", table).
		WithoutCache().
		WithFields("row_id", "address", "asset_id", "is_asset").
		Stream(ctx, func(r pack.Row) error {
			if err := r.Decode(md); err != nil {
				return err
			}

			// find next match; each address/asset combination is unique
			idx := -1
			for i := range match {
				if match[i].IsAsset != md.IsAsset {
					continue
				}
				if match[i].AssetId != md.AssetId {
					continue
				}
				if !match[i].Address.Equal(md.Address) {
					continue
				}
				idx = i
				break
			}

			// not found, ignore this table row
			if idx < 0 {
				return nil
			}

			// found, use row_id and remove from match set
			match[idx].RowId = md.RowId
			upd = append(upd, match[idx])
			match = append(match[:idx], match[idx+1:]...)
			if len(match) == 0 {
				return io.EOF
			}
			return nil
		})
	if err != nil && err != io.EOF {
		return err
	}

	// update
	if len(upd) > 0 {
		if err := table.Update(ctx, upd); err != nil {
			return err
		}
	}

	// insert remaining matches
	if len(match) > 0 {
		ins := make([]pack.Item, len(match))
		for i, v := range match {
			ins[i] = v
		}
		if err := table.Insert(ctx, ins); err != nil {
			return err
		}
	}

	return nil
}

func (m *Indexer) LookupConstant(ctx context.Context, hash tezos.ExprHash) (*model.Constant, error) {
	if !hash.IsValid() {
		return nil, ErrInvalidHash
	}
	table, err := m.Table(index.ConstantTableKey)
	if err != nil {
		return nil, err
	}
	cc := &model.Constant{}
	err = pack.NewQuery("constant_by_hash", table).
		AndEqual("address", hash.Bytes()).
		Execute(ctx, cc)
	if err != nil {
		return nil, err
	}
	if cc.RowId == 0 {
		return nil, index.ErrNoConstantEntry
	}
	return cc, nil
}

func (m *Indexer) FindPreviousStorage(ctx context.Context, id model.AccountID, since, until int64) (*model.Storage, error) {
	table, err := m.Table(index.StorageTableKey)
	if err != nil {
		return nil, err
	}
	store := &model.Storage{}
	err = pack.NewQuery("api.storage.find", table).
		WithLimit(1). // there should only be one match anyways
		WithDesc().   // search in reverse order to find latest update
		AndGte("height", since).
		AndLte("height", until).
		AndEqual("account_id", id).
		Execute(ctx, store)
	if err != nil {
		return nil, err
	}
	if store.RowId == 0 {
		return nil, index.ErrNoStorageEntry
	}
	return store, nil
}

func (m *Indexer) LookupStorage(ctx context.Context, id model.AccountID, h uint64, since, until int64) (*model.Storage, error) {
	table, err := m.Table(index.StorageTableKey)
	if err != nil {
		return nil, err
	}
	store := &model.Storage{}
	err = pack.NewQuery("api.storage.lookup", table).
		WithLimit(1). // there should only be one match anyways
		WithDesc().   // search in reverse order to find latest update
		AndGte("height", since).
		AndLte("height", until).
		AndEqual("account_id", id).
		AndEqual("hash", h).
		// WithStatsAfter(10).
		Execute(ctx, store)
	if err != nil {
		return nil, err
	}
	if store.RowId == 0 {
		return nil, index.ErrNoStorageEntry
	}
	return store, nil
}
