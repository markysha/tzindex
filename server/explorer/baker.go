// Copyright (c) 2020-2021 Blockwatch Data Inc.
// Author: alex@blockwatch.cc

package explorer

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"time"

	"github.com/echa/code/iso"
	"github.com/gorilla/mux"

	"blockwatch.cc/packdb/pack"
	"blockwatch.cc/packdb/util"
	"blockwatch.cc/packdb/vec"
	"blockwatch.cc/tzgo/tezos"
	"blockwatch.cc/tzindex/etl"
	"blockwatch.cc/tzindex/etl/index"
	"blockwatch.cc/tzindex/etl/model"
	"blockwatch.cc/tzindex/server"
)

func init() {
	server.Register(BakerList{})
}

type BakerStatistics struct {
	TotalRewardsEarned float64 `json:"total_rewards_earned"`
	TotalFeesEarned    float64 `json:"total_fees_earned"`
	TotalLost          float64 `json:"total_lost"`
	BlocksBaked        int64   `json:"blocks_baked"`
	BlocksProposed     int64   `json:"blocks_proposed"`
	BlocksNotBaked     int64   `json:"blocks_not_baked"`
	BlocksEndorsed     int64   `json:"blocks_endorsed"`
	BlocksNotEndorsed  int64   `json:"blocks_not_endorsed"`
	SlotsEndorsed      int64   `json:"slots_endorsed"`
	AvgLuck64          *int64  `json:"avg_luck_64,omitempty"`
	AvgPerformance64   *int64  `json:"avg_performance_64,omitempty"`
	AvgContribution64  *int64  `json:"avg_contribution_64,omitempty"`
	NBakerOps          int64   `json:"n_baker_ops"`
	NProposal          int64   `json:"n_proposals"`
	NBallot            int64   `json:"n_ballots"`
	NEndorsement       int64   `json:"n_endorsements"`
	NPreendorsement    int64   `json:"n_preendorsements"`
	NSeedNonce         int64   `json:"n_nonce_revelations"`
	N2Baking           int64   `json:"n_double_bakings"`
	N2Endorsement      int64   `json:"n_double_endorsements"`
	NAccusations       int64   `json:"n_accusations"`
	NSetDepositsLimit  int64   `json:"n_set_limits"`
}

type BakerEvents struct {
	LastBakeHeight    int64     `json:"last_bake_height"`
	LastBakeBlock     string    `json:"last_bake_block"`
	LastBakeTime      time.Time `json:"last_bake_time"`
	LastEndorseHeight int64     `json:"last_endorse_height"`
	LastEndorseBlock  string    `json:"last_endorse_block"`
	LastEndorseTime   time.Time `json:"last_endorse_time"`
	NextBakeHeight    int64     `json:"next_bake_height"`
	NextBakeTime      time.Time `json:"next_bake_time"`
	NextEndorseHeight int64     `json:"next_endorse_height"`
	NextEndorseTime   time.Time `json:"next_endorse_time"`
}

type Baker struct {
	Id                model.AccountID `json:"-"`
	Address           tezos.Address   `json:"address"`
	BakerSince        time.Time       `json:"baker_since"`
	BakerUntil        *time.Time      `json:"baker_until,omitempty"`
	GracePeriod       int64           `json:"grace_period"`
	BakerVersion      string          `json:"baker_version"`
	TotalBalance      float64         `json:"total_balance"`
	SpendableBalance  float64         `json:"spendable_balance"`
	FrozenBalance     float64         `json:"frozen_balance"`
	FrozenBond        float64         `json:"frozen_bond"`
	DelegatedBalance  float64         `json:"delegated_balance"`
	StakingBalance    float64         `json:"staking_balance"`
	ActiveStake       float64         `json:"active_stake"`
	StakingCapacity   float64         `json:"staking_capacity"`
	DepositsLimit     *float64        `json:"deposits_limit"`
	StakingShare      float64         `json:"staking_share"`
	ActiveDelegations int64           `json:"active_delegations"`
	TotalDelegations  int64           `json:"total_delegations"`
	IsFull            bool            `json:"is_full"`
	IsActive          bool            `json:"is_active"`

	Events   *BakerEvents     `json:"events,omitempty"`
	Stats    *BakerStatistics `json:"stats,omitempty"`
	Metadata *ShortMetadata   `json:"metadata,omitempty"`

	// caching
	expires time.Time `json:"-"`
	lastmod time.Time `json:"-"`
}

func NewBaker(ctx *server.Context, b *model.Baker, args server.Options) *Baker {
	tip := getTip(ctx)
	capacity := b.StakingCapacity(ctx.Params, 0)
	stake := b.ActiveStake(ctx.Params, 0)
	baker := &Baker{
		Id:                b.AccountId,
		Address:           b.Address,
		BakerSince:        ctx.Indexer.LookupBlockTime(ctx.Context, b.BakerSince),
		GracePeriod:       b.GracePeriod,
		BakerVersion:      hex.EncodeToString(b.GetVersionBytes()),
		TotalBalance:      ctx.Params.ConvertValue(b.TotalBalance()),
		SpendableBalance:  ctx.Params.ConvertValue(b.Account.SpendableBalance),
		FrozenBalance:     ctx.Params.ConvertValue(b.FrozenBalance()),
		FrozenBond:        ctx.Params.ConvertValue(b.Account.FrozenBond),
		DelegatedBalance:  ctx.Params.ConvertValue(b.DelegatedBalance),
		StakingBalance:    ctx.Params.ConvertValue(b.StakingBalance()),
		ActiveStake:       ctx.Params.ConvertValue(stake),
		StakingCapacity:   ctx.Params.ConvertValue(capacity),
		ActiveDelegations: b.ActiveDelegations,
		TotalDelegations:  b.TotalDelegations,
		IsActive:          b.IsActive,
		IsFull:            b.StakingBalance() >= capacity,
		StakingShare:      math.Ceil(float64(stake)/float64(tip.Supply.ActiveStake)*100_000) / 100_000,
		expires:           tip.Timestamp.Add(ctx.Params.BlockTime()),
		lastmod:           ctx.Indexer.LookupBlockTime(ctx.Context, b.Account.LastSeen),
	}

	if !b.IsActive {
		baker.BakerUntil = ctx.Indexer.LookupBlockTimePtr(ctx.Context, b.BakerUntil)
		baker.StakingShare = 0
	}

	if b.DepositsLimit >= 0 {
		baker.DepositsLimit = Float64Ptr(ctx.Params.ConvertValue(b.DepositsLimit))
	}

	if args.WithMeta() {
		// add statistics
		stats := BakerStatistics{
			TotalRewardsEarned: ctx.Params.ConvertValue(b.TotalRewardsEarned),
			TotalFeesEarned:    ctx.Params.ConvertValue(b.TotalFeesEarned),
			TotalLost:          ctx.Params.ConvertValue(b.TotalLost),
			BlocksBaked:        b.BlocksBaked,
			BlocksProposed:     b.BlocksProposed,
			BlocksNotBaked:     b.BlocksNotBaked,
			BlocksEndorsed:     b.BlocksEndorsed,
			BlocksNotEndorsed:  b.BlocksNotEndorsed,
			SlotsEndorsed:      b.SlotsEndorsed,
			NBakerOps:          b.NBakerOps,
			NProposal:          b.NProposal,
			NBallot:            b.NBallot,
			NEndorsement:       b.NEndorsement,
			NPreendorsement:    b.NPreendorsement,
			NSeedNonce:         b.NSeedNonce,
			N2Baking:           b.N2Baking,
			N2Endorsement:      b.N2Endorsement,
			NSetDepositsLimit:  b.NSetDepositsLimit,
			NAccusations:       b.NAccusations,
		}

		// get performance data
		recentCycle := ctx.Params.CycleFromHeight(b.Account.LastSeen) - 1
		if p, err := ctx.Indexer.BakerPerformance(ctx, b.AccountId, util.Max64(0, recentCycle-64), recentCycle); err == nil {
			stats.AvgLuck64 = &p[0]
			stats.AvgPerformance64 = &p[1]
			stats.AvgContribution64 = &p[2]
		}
		baker.Stats = &stats

		// add events
		if !ctx.Indexer.IsLightMode() {
			ev := BakerEvents{}
			if info, err := ctx.Indexer.LookupLastBakedBlock(ctx, b); err == nil {
				ev.LastBakeHeight = info.Height
				ev.LastBakeBlock = info.Hash.String()
				ev.LastBakeTime = info.Timestamp
			}

			if info, err := ctx.Indexer.LookupLastEndorsedBlock(ctx, b); err == nil {
				ev.LastEndorseHeight = info.Height
				ev.LastEndorseBlock = info.Hash.String()
				ev.LastEndorseTime = info.Timestamp
			}

			if b.IsActive {
				// from rights cache
				bh, eh := ctx.Indexer.NextRights(ctx, b.AccountId, tip.Height)
				if bh > 0 {
					ev.NextBakeHeight = bh
					ev.NextBakeTime = tip.Timestamp.Add(ctx.Params.BlockTime() * time.Duration(bh-tip.Height))
				}
				if eh > 0 {
					ev.NextEndorseHeight = eh
					ev.NextEndorseTime = tip.Timestamp.Add(ctx.Params.BlockTime() * time.Duration(eh-tip.Height))
				}
			}
			baker.Events = &ev
		}

		// add metadata
		if md, ok := lookupMetadataById(ctx, b.AccountId, 0, false); ok {
			baker.Metadata = md.Short()
		}
	}

	return baker
}

var _ server.RESTful = (*BakerList)(nil)
var _ server.Resource = (*BakerList)(nil)

func (_ BakerList) RESTPrefix() string {
	return "/explorer/bakers"
}

func (l BakerList) RESTPath(r *mux.Router) string {
	return l.RESTPrefix()
}

func (a BakerList) RegisterDirectRoutes(r *mux.Router) error {
	r.HandleFunc(a.RESTPrefix(), server.C(ListBakers)).Methods("GET")
	return nil
}

func (b BakerList) RegisterRoutes(r *mux.Router) error {
	r.HandleFunc("/{ident}", server.C(ReadBaker)).Methods("GET").Name("baker")
	r.HandleFunc("/{ident}/votes", server.C(ListBakerVotes)).Methods("GET")
	r.HandleFunc("/{ident}/endorsements", server.C(ListBakerEndorsements)).Methods("GET")
	r.HandleFunc("/{ident}/delegations", server.C(ListBakerDelegations)).Methods("GET")
	r.HandleFunc("/{ident}/income/{cycle}", server.C(GetBakerIncome)).Methods("GET")
	r.HandleFunc("/{ident}/rights/{cycle}", server.C(GetBakerRights)).Methods("GET")
	r.HandleFunc("/{ident}/snapshot/{cycle}", server.C(GetBakerSnapshot)).Methods("GET")
	r.HandleFunc("/{ident}/metadata", server.C(ReadMetadata)).Methods("GET")
	return nil
}

type BakerListRequest struct {
	ListRequest
	Active  bool           `schema:"active"`
	Status  *string        `schema:"status"`
	Country *iso.Country   `schema:"country"`
	Suggest *tezos.Address `schema:"suggest"`
}

type BakerList struct {
	list     []Baker
	expires  time.Time
	modified time.Time
}

func (l BakerList) MarshalJSON() ([]byte, error) { return json.Marshal(l.list) }
func (l BakerList) LastModified() time.Time      { return l.modified }
func (l BakerList) Expires() time.Time           { return l.expires }

func ListBakers(ctx *server.Context) (interface{}, int) {
	args := &BakerListRequest{}
	ctx.ParseRequestArgs(args)

	// load suggest account
	var suggest *model.Account
	if args.Suggest != nil {
		var err error
		if suggest, err = ctx.Indexer.LookupAccount(ctx.Context, *args.Suggest); err != nil {
			switch err {
			case index.ErrNoAccountEntry:
				panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no such account", err))
			default:
				panic(server.EInternal(server.EC_DATABASE, err.Error(), nil))
			}
		}

		// ensure account is not empty
		if !suggest.IsFunded {
			panic(server.ENotFound(server.EC_RESOURCE_CONFLICT, "account is not funded", nil))
		}
		if suggest.IsBaker {
			panic(server.ENotFound(server.EC_RESOURCE_CONFLICT, "account is not delegatable", nil))
		}

		// suggestions are limited to public bakers
		status := "public"
		args.Active = true
		args.Status = &status
		args.Limit = ctx.Cfg.ClampExplore(args.Limit)
	}

	// load list of all current bakers (no limit to keep loading logic simple
	// when used in combination with suggest feature)
	bakers, err := ctx.Indexer.ListBakers(ctx.Context, args.Active)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot list bakers", err))
	}
	// log.Infof("Found %d active bakers", len(bakers))

	var netRolls int64 // zero, compatibility only

	// get chain data from cache
	tip := getTip(ctx)
	netStake := tip.Supply.ActiveStake

	// get current alias data
	meta := allMetadataById(ctx)

	// prepare response lists
	bkr := make([]Baker, 0)

	// filter bakers
	for _, v := range bakers {
		// filter by alias attributes
		alias, hasAlias := meta[v.AccountId.Value()]
		if hasAlias {
			if args.Status != nil && *args.Status != alias.Status {
				// log.Infof("Skip %s status %s", v, alias.Status)
				continue
			}
			if args.Country != nil && *args.Country != alias.Country {
				// log.Infof("Skip %s country %s", v, alias.Country)
				continue
			}
			// filter by suggestion attributes
			if suggest != nil {
				if alias.MinDelegation > 0 && ctx.Params.ConvertValue(suggest.Balance()) < alias.MinDelegation {
					// log.Infof("Skip %s balance < min %d", v, suggest.Balance())
					continue
				}
			}
		} else {
			// if no alias is known
			if args.Status != nil {
				// log.Infof("Skip %s non status", v)
				continue
			}
			if args.Country != nil {
				// log.Infof("Skip %s non country", v)
				continue
			}
		}

		if suggest != nil {
			// skip non-delegatable bakers
			if alias.NonDelegatable {
				continue
			}
			// filter by capacity
			if suggest.Balance() > v.StakingCapacity(ctx.Params, netRolls)-v.StakingBalance() {
				// log.Infof("Skip %s capacity %d < %d",
				// 	v,
				// 	v.StakingCapacity(ctx.Params, netRolls)-v.StakingBalance(),
				// 	suggest.Balance(),
				// )
				continue
			}
			// remove the current baker, if any
			if suggest.BakerId == v.AccountId {
				continue
			}
		}

		// apply offset and cursor (only in non-suggest mode)
		if suggest == nil {
			if args.Offset > 0 {
				// log.Infof("Skip %s offset %d", v, args.Offset)
				args.Offset--
				continue
			}
			if args.Cursor > 0 && v.RowId.Value() <= args.Cursor {
				// log.Infof("Skip %s cursor %d", v, v.RowId)
				continue
			}
		}

		// build result
		capacity := v.StakingCapacity(ctx.Params, netRolls)
		stake := v.ActiveStake(ctx.Params, netRolls)
		baker := Baker{
			Id:                v.AccountId,
			GracePeriod:       v.GracePeriod,
			Address:           v.Address,
			BakerSince:        ctx.Indexer.LookupBlockTime(ctx.Context, v.BakerSince),
			BakerVersion:      hex.EncodeToString(v.GetVersionBytes()),
			TotalBalance:      ctx.Params.ConvertValue(v.TotalBalance()),
			SpendableBalance:  ctx.Params.ConvertValue(v.Account.SpendableBalance),
			FrozenBalance:     ctx.Params.ConvertValue(v.FrozenBalance()),
			FrozenBond:        ctx.Params.ConvertValue(v.Account.FrozenBond),
			DelegatedBalance:  ctx.Params.ConvertValue(v.DelegatedBalance),
			ActiveStake:       ctx.Params.ConvertValue(stake),
			StakingBalance:    ctx.Params.ConvertValue(v.StakingBalance()),
			StakingCapacity:   ctx.Params.ConvertValue(capacity),
			ActiveDelegations: v.ActiveDelegations,
			TotalDelegations:  v.TotalDelegations,
			IsFull:            v.StakingBalance() >= capacity,
			IsActive:          v.IsActive,
			StakingShare:      math.Ceil(float64(stake)/float64(netStake)*100_000) / 100_000,
			Stats: &BakerStatistics{
				TotalRewardsEarned: ctx.Params.ConvertValue(v.TotalRewardsEarned),
				TotalFeesEarned:    ctx.Params.ConvertValue(v.TotalFeesEarned),
				TotalLost:          ctx.Params.ConvertValue(v.TotalLost),
				BlocksBaked:        v.BlocksBaked,
				BlocksProposed:     v.BlocksProposed,
				BlocksNotBaked:     v.BlocksNotBaked,
				BlocksEndorsed:     v.BlocksEndorsed,
				BlocksNotEndorsed:  v.BlocksNotEndorsed,
				SlotsEndorsed:      v.SlotsEndorsed,
				NBakerOps:          v.NBakerOps,
				NProposal:          v.NProposal,
				NBallot:            v.NBallot,
				NEndorsement:       v.NEndorsement,
				NPreendorsement:    v.NPreendorsement,
				NSeedNonce:         v.NSeedNonce,
				N2Baking:           v.N2Baking,
				N2Endorsement:      v.N2Endorsement,
				NSetDepositsLimit:  v.NSetDepositsLimit,
				NAccusations:       v.NAccusations,
			},
		}

		if !v.IsActive {
			baker.BakerUntil = ctx.Indexer.LookupBlockTimePtr(ctx.Context, v.BakerUntil)
			baker.StakingShare = 0
		}

		// attach alias and append to lists
		if hasAlias {
			baker.Metadata = alias.Short()
			bkr = append(bkr, baker)
		} else {
			bkr = append(bkr, baker)
		}

		// apply limit only when not in suggest mode (need all results for randomization)
		if suggest == nil && args.Limit > 0 && len(bkr) == int(args.Limit) {
			break
		}
	}

	// build result
	resp := &BakerList{
		list:     make([]Baker, 0),
		modified: tip.Timestamp,
		expires:  ctx.Now,
	}

	// only cache non-randomized results
	if suggest == nil {
		resp.expires = tip.Timestamp.Add(ctx.Params.BlockTime())
	}

	// randomize suggestion: <=50% sponsored
	if args.Limit > 0 && suggest != nil {
		for args.Limit > 0 {
			// draw random from other
			idx := rand.Intn(len(bkr))
			resp.list = append(resp.list, bkr[idx])
			bkr = append(bkr[:idx], bkr[idx+1:]...)
			args.Limit--
		}
	} else {
		resp.list = bkr
		if args.Limit > 0 {
			resp.list = resp.list[:util.Min(int(args.Limit), len(resp.list))]
		}
		if args.Order == pack.OrderAsc {
			sort.Slice(resp.list, func(i, j int) bool { return resp.list[i].Id < resp.list[j].Id })
		} else {
			sort.Slice(resp.list, func(i, j int) bool { return resp.list[i].Id > resp.list[j].Id })
		}
	}

	// log.Infof("Final %d active bakers cycle %d", len(resp.list), tip.Cycle)

	// add expensive performance data
	for i, v := range resp.list {
		if p, err := ctx.Indexer.BakerPerformance(ctx, v.Id, util.Max64(0, tip.Cycle-64), tip.Cycle); err == nil {
			resp.list[i].Stats.AvgLuck64 = &p[0]
			resp.list[i].Stats.AvgPerformance64 = &p[1]
			resp.list[i].Stats.AvgContribution64 = &p[2]
		}
	}
	return resp, http.StatusOK
}

func loadBaker(ctx *server.Context) *model.Baker {
	if accIdent, ok := mux.Vars(ctx.Request)["ident"]; !ok || accIdent == "" {
		panic(server.EBadRequest(server.EC_RESOURCE_ID_MISSING, "missing baker address", nil))
	} else {
		addr, err := tezos.ParseAddress(accIdent)
		if err != nil {
			panic(server.EBadRequest(server.EC_RESOURCE_ID_MALFORMED, "invalid address", err))
		}
		bkr, err := ctx.Indexer.LookupBaker(ctx, addr)
		if err != nil {
			switch err {
			case index.ErrNoBakerEntry:
				panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no such account", err))
			default:
				panic(server.EInternal(server.EC_DATABASE, err.Error(), nil))
			}
		}
		return bkr
	}
}

func ReadBaker(ctx *server.Context) (interface{}, int) {
	args := &AccountRequest{}
	ctx.ParseRequestArgs(args)
	return NewBaker(ctx, loadBaker(ctx), args), http.StatusOK
}

func ListBakerVotes(ctx *server.Context) (interface{}, int) {
	args := &OpsRequest{
		ListRequest: ListRequest{
			Order: pack.OrderDesc,
		},
	}
	ctx.ParseRequestArgs(args)
	acc := loadAccount(ctx)

	r := etl.ListRequest{
		Account: acc,
		Since:   args.SinceHeight,
		Until:   args.BlockHeight,
		Offset:  args.Offset,
		Limit:   ctx.Cfg.ClampExplore(args.Limit),
		Cursor:  args.Cursor,
		Order:   args.Order,
	}

	// fetch ballots
	ballots, err := ctx.Indexer.ListBallots(ctx, r)
	if err != nil {
		switch err {
		case etl.ErrNoTable:
			panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "cannot access ballots table", err))
		default:
			panic(server.EInternal(server.EC_DATABASE, "cannot read account ballots", err))
		}
	}

	// fetch op hashes for each ballot
	oids := make([]uint64, 0)
	for _, v := range ballots {
		oids = append(oids, v.OpId.Value())
	}

	// lookup
	ops, err := ctx.Indexer.LookupOpIds(ctx, vec.UniqueUint64Slice(oids))
	if err != nil && err != index.ErrNoOpEntry {
		panic(server.EInternal(server.EC_DATABASE, "cannot read ops for ballots", err))
	}

	// prepare for lookup
	opMap := make(map[model.OpID]tezos.OpHash)
	for _, v := range ops {
		opMap[v.RowId] = v.Hash
	}
	ops = nil
	ebs := make([]*Ballot, len(ballots))
	for i, v := range ballots {
		o, _ := opMap[v.OpId]
		ebs[i] = NewBallot(ctx, v, ctx.Indexer.LookupProposalHash(ctx, v.ProposalId), o)
	}
	return ebs, http.StatusOK
}

func ListBakerEndorsements(ctx *server.Context) (interface{}, int) {
	args := &OpsRequest{
		ListRequest: ListRequest{
			Order: pack.OrderDesc,
		},
	}
	ctx.ParseRequestArgs(args)
	acc := loadAccount(ctx)

	r := etl.ListRequest{
		Account: acc,
		Mode:    pack.FilterModeEqual,
		Since:   args.SinceHeight,
		Until:   args.BlockHeight,
		Offset:  args.Offset,
		Limit:   ctx.Cfg.ClampExplore(args.Limit),
		Cursor:  args.Cursor,
		Order:   args.Order,
	}

	if args.Sender.IsValid() {
		if a, err := ctx.Indexer.LookupAccount(ctx.Context, args.Sender); err != nil {
			panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no such sender account", err))
		} else {
			r.SenderId = a.RowId
		}
	}

	ops, err := ctx.Indexer.ListBakerEndorsements(ctx, r)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read endorsements", err))
	}

	resp := make(OpList, 0)
	cache := make(map[int64]interface{})
	for _, v := range ops {
		resp.Append(NewOp(ctx, v, nil, nil, args, cache), args.WithMerge())
	}
	return resp, http.StatusOK
}

func ListBakerDelegations(ctx *server.Context) (interface{}, int) {
	args := &OpsRequest{
		ListRequest: ListRequest{
			Order: pack.OrderDesc,
		},
	}
	ctx.ParseRequestArgs(args)
	acc := loadAccount(ctx)

	r := etl.ListRequest{
		Account: acc,
		// ReceiverId: acc.RowId,
		Mode:   pack.FilterModeEqual,
		Typs:   []model.OpType{model.OpTypeDelegation},
		Since:  args.SinceHeight,
		Until:  args.BlockHeight,
		Offset: args.Offset,
		Limit:  ctx.Cfg.ClampExplore(args.Limit),
		Cursor: args.Cursor,
		Order:  args.Order,
	}

	if args.Sender.IsValid() {
		if a, err := ctx.Indexer.LookupAccount(ctx.Context, args.Sender); err != nil {
			panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no such sender account", err))
		} else {
			r.SenderId = a.RowId
		}
	}

	ops, err := ctx.Indexer.ListAccountOps(ctx, r)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read account operations", err))
	}

	resp := make(OpList, 0)
	cache := make(map[int64]interface{})
	for _, v := range ops {
		resp.Append(NewOp(ctx, v, nil, nil, args, cache), args.WithMerge())
	}
	return resp, http.StatusOK
}

type ExplorerRights struct {
	Address  tezos.Address `json:"address"`
	Cycle    int64         `json:"cycle"`
	Height   int64         `json:"start_height"`
	Bake     string        `json:"baking_rights"`
	Endorse  string        `json:"endorsing_rights"`
	Baked    string        `json:"blocks_baked"`
	Endorsed string        `json:"blocks_endorsed"`
	Seed     string        `json:"seeds_required"`
	Seeded   string        `json:"seeds_revealed"`
}

func GetBakerRights(ctx *server.Context) (interface{}, int) {
	acc := loadBaker(ctx)
	cycle := parseCycle(ctx)

	table, err := ctx.Indexer.Table(index.RightsTableKey)
	if err != nil {
		panic(server.ENotFound(server.EC_DATABASE, "missing rights table", err))
	}
	var right model.Right
	err = pack.NewQuery("get_baker_rights", table).
		AndEqual("account_id", acc.AccountId).
		AndEqual("cycle", cycle).
		WithLimit(1).
		Execute(ctx.Context, &right)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read rights", err))
	}
	if right.RowId == 0 {
		panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no rights for cycle", nil))
	}
	resp := &ExplorerRights{
		Address:  acc.Address,
		Cycle:    cycle,
		Height:   ctx.Params.CycleStartHeight(cycle),
		Bake:     right.Bake.String(),
		Endorse:  right.Endorse.String(),
		Baked:    right.Baked.String(),
		Endorsed: right.Endorsed.String(),
		Seed:     right.Seed.String(),
		Seeded:   right.Seeded.String(),
	}
	return resp, http.StatusOK
}

type ExplorerIncome struct {
	Cycle                  int64   `json:"cycle"`
	Rolls                  int64   `json:"snapshot_rolls"`
	Balance                float64 `json:"own_balance"`
	Delegated              float64 `json:"delegated_balance"`
	Staking                float64 `json:"staking_balance"`
	ActiveStake            float64 `json:"active_stake"`
	NDelegations           int64   `json:"n_delegations"`
	NBakingRights          int64   `json:"n_baking_rights"`
	NEndorsingRights       int64   `json:"n_endorsing_rights"`
	Luck                   float64 `json:"luck"`                 // coins by fair share of rolls
	LuckPct                int64   `json:"luck_percent"`         // 0.0 .. +N.00 by fair share of rolls
	ContributionPct        int64   `json:"contribution_percent"` // 0.0 .. +N.00 by rights utilized
	PerformancePct         int64   `json:"performance_percent"`  // -N.00 .. +N.00 by expected income
	NBlocksBaked           int64   `json:"n_blocks_baked"`
	NBlocksProposed        int64   `json:"n_blocks_proposed"`
	NBlocksNotBaked        int64   `json:"n_blocks_not_baked"`
	NBlocksEndorsed        int64   `json:"n_blocks_endorsed"`
	NBlocksNotEndorsed     int64   `json:"n_blocks_not_endorsed"`
	NSlotsEndorsed         int64   `json:"n_slots_endorsed"`
	NSeedsRevealed         int64   `json:"n_seeds_revealed"`
	ExpectedIncome         float64 `json:"expected_income"`
	TotalIncome            float64 `json:"total_income"`
	TotalDeposits          float64 `json:"total_deposits"`
	BakingIncome           float64 `json:"baking_income"`
	EndorsingIncome        float64 `json:"endorsing_income"`
	AccusationIncome       float64 `json:"accusation_income"`
	SeedIncome             float64 `json:"seed_income"`
	FeesIncome             float64 `json:"fees_income"`
	TotalLoss              float64 `json:"total_loss"`
	AccusationLoss         float64 `json:"accusation_loss"`
	SeedLoss               float64 `json:"seed_loss"`
	EndorsingLoss          float64 `json:"endorsing_loss"`
	LostAccusationFees     float64 `json:"lost_accusation_fees"`
	LostAccusationRewards  float64 `json:"lost_accusation_rewards"`
	LostAccusationDeposits float64 `json:"lost_accusation_deposits"`
	LostSeedFees           float64 `json:"lost_seed_fees"`
	LostSeedRewards        float64 `json:"lost_seed_rewards"`
}

func GetBakerIncome(ctx *server.Context) (interface{}, int) {
	acc := loadBaker(ctx)
	cycle := parseCycle(ctx)

	table, err := ctx.Indexer.Table(index.IncomeTableKey)
	if err != nil {
		panic(server.ENotFound(server.EC_DATABASE, "missing income table", err))
	}
	var income model.Income
	err = pack.NewQuery("get_baker_income", table).
		AndEqual("account_id", acc.AccountId).
		AndEqual("cycle", cycle).
		WithLimit(1).
		Execute(ctx.Context, &income)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read income", err))
	}
	if income.RowId == 0 {
		panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no income for cycle", nil))
	}

	resp := &ExplorerIncome{
		Cycle:                  cycle,
		Rolls:                  income.Rolls,
		Balance:                ctx.Params.ConvertValue(income.Balance),
		Delegated:              ctx.Params.ConvertValue(income.Delegated),
		Staking:                ctx.Params.ConvertValue(income.Balance + income.Delegated),
		ActiveStake:            ctx.Params.ConvertValue(income.ActiveStake),
		NDelegations:           income.NDelegations,
		NBakingRights:          income.NBakingRights,
		NEndorsingRights:       income.NEndorsingRights,
		Luck:                   ctx.Params.ConvertValue(income.Luck),
		LuckPct:                income.LuckPct,
		ContributionPct:        income.ContributionPct,
		PerformancePct:         income.PerformancePct,
		NBlocksBaked:           income.NBlocksBaked,
		NBlocksProposed:        income.NBlocksProposed,
		NBlocksNotBaked:        income.NBlocksNotBaked,
		NBlocksEndorsed:        income.NBlocksEndorsed,
		NBlocksNotEndorsed:     income.NBlocksNotEndorsed,
		NSlotsEndorsed:         income.NSlotsEndorsed,
		NSeedsRevealed:         income.NSeedsRevealed,
		ExpectedIncome:         ctx.Params.ConvertValue(income.ExpectedIncome),
		TotalIncome:            ctx.Params.ConvertValue(income.TotalIncome),
		TotalDeposits:          ctx.Params.ConvertValue(income.TotalDeposits),
		BakingIncome:           ctx.Params.ConvertValue(income.BakingIncome),
		EndorsingIncome:        ctx.Params.ConvertValue(income.EndorsingIncome),
		AccusationIncome:       ctx.Params.ConvertValue(income.AccusationIncome),
		SeedIncome:             ctx.Params.ConvertValue(income.SeedIncome),
		FeesIncome:             ctx.Params.ConvertValue(income.FeesIncome),
		TotalLoss:              ctx.Params.ConvertValue(income.TotalLoss),
		AccusationLoss:         ctx.Params.ConvertValue(income.AccusationLoss),
		SeedLoss:               ctx.Params.ConvertValue(income.SeedLoss),
		EndorsingLoss:          ctx.Params.ConvertValue(income.EndorsingLoss),
		LostAccusationFees:     ctx.Params.ConvertValue(income.LostAccusationFees),
		LostAccusationRewards:  ctx.Params.ConvertValue(income.LostAccusationRewards),
		LostAccusationDeposits: ctx.Params.ConvertValue(income.LostAccusationDeposits),
		LostSeedFees:           ctx.Params.ConvertValue(income.LostSeedFees),
		LostSeedRewards:        ctx.Params.ConvertValue(income.LostSeedRewards),
	}
	return resp, http.StatusOK
}

type ExplorerDelegator struct {
	Address  tezos.Address `json:"address"`
	Balance  int64         `json:"balance"`
	IsFunded bool          `json:"is_funded"`
}

type ExplorerSnapshot struct {
	BakeCycle              int64               `json:"baking_cycle"`
	Height                 int64               `json:"snapshot_height"`
	Cycle                  int64               `json:"snapshot_cycle"`
	Timestamp              time.Time           `json:"snapshot_time"`
	Index                  int                 `json:"snapshot_index"`
	Rolls                  int64               `json:"snapshot_rolls"`
	ActiveStake            int64               `json:"active_stake"`
	StakingBalance         int64               `json:"staking_balance"`
	OwnBalance             int64               `json:"own_balance"`
	DelegatedBalance       int64               `json:"delegated_balance"`
	NDelegations           int64               `json:"n_delegations"`
	ExpectedIncome         int64               `json:"expected_income"`
	TotalIncome            int64               `json:"total_income"`
	TotalDeposits          int64               `json:"total_deposits"`
	BakingIncome           int64               `json:"baking_income"`
	EndorsingIncome        int64               `json:"endorsing_income"`
	AccusationIncome       int64               `json:"accusation_income"`
	SeedIncome             int64               `json:"seed_income"`
	FeesIncome             int64               `json:"fees_income"`
	TotalLoss              int64               `json:"total_loss"`
	AccusationLoss         int64               `json:"accusation_loss"`
	SeedLoss               int64               `json:"seed_loss"`
	EndorsingLoss          int64               `json:"endorsing_loss"`
	LostAccusationFees     int64               `json:"lost_accusation_fees"`
	LostAccusationRewards  int64               `json:"lost_accusation_rewards"`
	LostAccusationDeposits int64               `json:"lost_accusation_deposits"`
	LostSeedFees           int64               `json:"lost_seed_fees"`
	LostSeedRewards        int64               `json:"lost_seed_rewards"`
	Delegators             []ExplorerDelegator `json:"delegators"`
}

func GetBakerSnapshot(ctx *server.Context) (interface{}, int) {
	acc := loadBaker(ctx)
	cycle := parseCycle(ctx)
	baseCycle := ctx.Params.ForCycle(cycle).SnapshotBaseCycle(cycle)

	snapshotTable, err := ctx.Indexer.Table(index.SnapshotTableKey)
	if err != nil {
		panic(server.ENotFound(server.EC_DATABASE, "missing snapshot table", err))
	}

	// get baker
	var self model.Snapshot
	err = pack.NewQuery("api.baker.snapshot", snapshotTable).
		AndEqual("account_id", acc.AccountId).
		AndEqual("cycle", baseCycle).
		AndEqual("is_selected", true).
		AndEqual("is_baker", true).
		Execute(ctx.Context, &self)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read snapshot", err))
	}
	if self.RowId == 0 {
		panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no cycle snapshot", nil))
	}

	// get income
	incomeTable, err := ctx.Indexer.Table(index.IncomeTableKey)
	if err != nil {
		panic(server.ENotFound(server.EC_DATABASE, "missing income table", err))
	}
	var income model.Income
	err = pack.NewQuery("api.baker.income", incomeTable).
		AndEqual("account_id", acc.AccountId).
		AndEqual("cycle", cycle).
		WithLimit(1).
		Execute(ctx.Context, &income)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read income", err))
	}
	if income.RowId == 0 {
		panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, "no income for cycle", nil))
	}

	// list delegators
	snaps := make([]model.Snapshot, 0)
	err = pack.NewQuery("api.baker.delegators", snapshotTable).
		AndEqual("baker_id", acc.AccountId).
		AndEqual("cycle", baseCycle).
		AndEqual("is_selected", true).
		AndEqual("is_baker", false).
		WithFields("account_id", "balance").
		Execute(ctx.Context, &snaps)

	// list funding state
	ids := make([]uint64, len(snaps))
	for i, v := range snaps {
		ids[i] = v.AccountId.Value()
	}
	type XAcc struct {
		RowId    model.AccountID `pack:"I"`
		IsFunded bool            `pack:"f"`
	}
	accs := make([]*XAcc, 0)
	accountTable, err := ctx.Indexer.Table(index.AccountTableKey)
	if err != nil {
		panic(server.ENotFound(server.EC_DATABASE, "missing account table", err))
	}
	err = pack.NewQuery("api.baker.delegator_status", accountTable).
		AndIn("row_id", ids).
		WithFields("row_id", "is_funded").
		Execute(ctx.Context, &accs)
	if err != nil {
		panic(server.EInternal(server.EC_DATABASE, "cannot read accounts", err))
	}
	isFunded := make(map[model.AccountID]bool)
	for _, v := range accs {
		isFunded[v.RowId] = v.IsFunded
	}

	resp := &ExplorerSnapshot{
		BakeCycle:              cycle,
		Height:                 self.Height,
		Cycle:                  self.Cycle,
		Timestamp:              self.Timestamp,
		Index:                  self.Index,
		Rolls:                  self.Rolls,
		ActiveStake:            self.ActiveStake,
		StakingBalance:         self.Balance + self.Delegated,
		OwnBalance:             self.Balance,
		DelegatedBalance:       self.Delegated,
		NDelegations:           self.NDelegations,
		ExpectedIncome:         income.ExpectedIncome,
		TotalIncome:            income.TotalIncome,
		TotalDeposits:          income.TotalDeposits,
		BakingIncome:           income.BakingIncome,
		EndorsingIncome:        income.EndorsingIncome,
		AccusationIncome:       income.AccusationIncome,
		SeedIncome:             income.SeedIncome,
		FeesIncome:             income.FeesIncome,
		TotalLoss:              income.TotalLoss,
		AccusationLoss:         income.AccusationLoss,
		SeedLoss:               income.SeedLoss,
		EndorsingLoss:          income.EndorsingLoss,
		LostAccusationFees:     income.LostAccusationFees,
		LostAccusationRewards:  income.LostAccusationRewards,
		LostAccusationDeposits: income.LostAccusationDeposits,
		LostSeedFees:           income.LostSeedFees,
		LostSeedRewards:        income.LostSeedRewards,
		Delegators:             make([]ExplorerDelegator, len(snaps)),
	}
	for i, v := range snaps {
		resp.Delegators[i].Address = ctx.Indexer.LookupAddress(ctx.Context, v.AccountId)
		resp.Delegators[i].Balance = v.Balance
		resp.Delegators[i].IsFunded = isFunded[v.AccountId]
	}

	// sort delegators by balance
	sort.Slice(resp.Delegators, func(i, j int) bool { return resp.Delegators[i].Balance > resp.Delegators[j].Balance })
	return resp, http.StatusOK
}
