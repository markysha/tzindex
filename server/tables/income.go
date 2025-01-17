// Copyright (c) 2020-2021 Blockwatch Data Inc.
// Author: alex@blockwatch.cc

package tables

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"blockwatch.cc/packdb/encoding/csv"
	"blockwatch.cc/packdb/pack"
	"blockwatch.cc/packdb/util"
	"blockwatch.cc/tzgo/tezos"
	"blockwatch.cc/tzindex/etl/index"
	"blockwatch.cc/tzindex/etl/model"
	"blockwatch.cc/tzindex/server"
)

var (
	// long -> short form
	incomeSourceNames map[string]string
	// short -> long form
	incomeAliasNames map[string]string
	// all aliases as list
	incomeAllAliases []string
)

func init() {
	fields, err := pack.Fields(&model.Income{})
	if err != nil {
		log.Fatalf("imcome field type error: %v\n", err)
	}
	incomeSourceNames = fields.NameMapReverse()
	incomeAllAliases = fields.Aliases()

	// add extra translations
	incomeSourceNames["address"] = "A"
	incomeSourceNames["time"] = "c"
	incomeSourceNames["start_time"] = "c"
	incomeSourceNames["end_time"] = "c"
	incomeAllAliases = append(incomeAllAliases, "address", "start_time", "end_time")
}

// configurable marshalling helper
type Income struct {
	model.Income
	verbose bool            // cond. marshal
	columns util.StringList // cond. cols & order when brief
	params  *tezos.Params   // blockchain amount conversion
	ctx     *server.Context
}

func (r *Income) MarshalJSON() ([]byte, error) {
	if r.verbose {
		return r.MarshalJSONVerbose()
	} else {
		return r.MarshalJSONBrief()
	}
}

func (c *Income) MarshalJSONVerbose() ([]byte, error) {
	inc := struct {
		RowId                  uint64    `json:"row_id"`
		Cycle                  int64     `json:"cycle"`
		AccountId              uint64    `json:"account_id"`
		Address                string    `json:"address"`
		Rolls                  int64     `json:"rolls"`
		Balance                float64   `json:"balance"`
		Delegated              float64   `json:"delegated"`
		ActiveStake            float64   `json:"active_stake"`
		NDelegations           int64     `json:"n_delegations"`
		NBakingRights          int64     `json:"n_baking_rights"`
		NEndorsingRights       int64     `json:"n_endorsing_rights"`
		Luck                   float64   `json:"luck"`
		LuckPct                float64   `json:"luck_percent"`
		ContributionPct        float64   `json:"contribution_percent"`
		PerformancePct         float64   `json:"performance_percent"`
		NBlocksBaked           int64     `json:"n_blocks_baked"`
		NBlocksProposed        int64     `json:"n_blocks_proposed"`
		NBlocksNotBaked        int64     `json:"n_blocks_not_baked"`
		NBlocksEndorsed        int64     `json:"n_blocks_endorsed"`
		NBlocksNotEndorsed     int64     `json:"n_blocks_not_endorsed"`
		NSlotsEndorsed         int64     `json:"n_slots_endorsed"`
		NSeedsRevealed         int64     `json:"n_seeds_revealed"`
		ExpectedIncome         float64   `json:"expected_income"`
		TotalIncome            float64   `json:"total_income"`
		TotalDeposits          float64   `json:"total_deposits"`
		BakingIncome           float64   `json:"baking_income"`
		EndorsingIncome        float64   `json:"endorsing_income"`
		AccusationIncome       float64   `json:"accusation_income"`
		SeedIncome             float64   `json:"seed_income"`
		FeesIncome             float64   `json:"fees_income"`
		TotalLoss              float64   `json:"total_loss"`
		AccusationLoss         float64   `json:"accusation_loss"`
		SeedLoss               float64   `json:"seed_loss"`
		EndorsingLoss          float64   `json:"endorsing_loss"`
		LostAccusationFees     float64   `json:"lost_accusation_fees"`
		LostAccusationRewards  float64   `json:"lost_accusation_rewards"`
		LostAccusationDeposits float64   `json:"lost_accusation_deposits"`
		LostSeedFees           float64   `json:"lost_seed_fees"`
		LostSeedRewards        float64   `json:"lost_seed_rewards"`
		StartTime              time.Time `json:"start_time"`
		EndTime                time.Time `json:"end_time"`
	}{
		RowId:                  c.RowId,
		Cycle:                  c.Cycle,
		AccountId:              c.AccountId.Value(),
		Address:                c.ctx.Indexer.LookupAddress(c.ctx, c.AccountId).String(),
		Rolls:                  c.Rolls,
		Balance:                c.params.ConvertValue(c.Balance),
		Delegated:              c.params.ConvertValue(c.Delegated),
		ActiveStake:            c.params.ConvertValue(c.ActiveStake),
		NDelegations:           c.NDelegations,
		NBakingRights:          c.NBakingRights,
		NEndorsingRights:       c.NEndorsingRights,
		Luck:                   c.params.ConvertValue(c.Luck),
		LuckPct:                float64(c.LuckPct) / 100,
		ContributionPct:        float64(c.ContributionPct) / 100,
		PerformancePct:         float64(c.PerformancePct) / 100,
		NBlocksBaked:           c.NBlocksBaked,
		NBlocksProposed:        c.NBlocksProposed,
		NBlocksNotBaked:        c.NBlocksNotBaked,
		NBlocksEndorsed:        c.NBlocksEndorsed,
		NBlocksNotEndorsed:     c.NBlocksNotEndorsed,
		NSlotsEndorsed:         c.NSlotsEndorsed,
		NSeedsRevealed:         c.NSeedsRevealed,
		ExpectedIncome:         c.params.ConvertValue(c.ExpectedIncome),
		TotalIncome:            c.params.ConvertValue(c.TotalIncome),
		TotalDeposits:          c.params.ConvertValue(c.TotalDeposits),
		BakingIncome:           c.params.ConvertValue(c.BakingIncome),
		EndorsingIncome:        c.params.ConvertValue(c.EndorsingIncome),
		AccusationIncome:       c.params.ConvertValue(c.AccusationIncome),
		SeedIncome:             c.params.ConvertValue(c.SeedIncome),
		FeesIncome:             c.params.ConvertValue(c.FeesIncome),
		TotalLoss:              c.params.ConvertValue(c.TotalLoss),
		AccusationLoss:         c.params.ConvertValue(c.AccusationLoss),
		SeedLoss:               c.params.ConvertValue(c.SeedLoss),
		EndorsingLoss:          c.params.ConvertValue(c.EndorsingLoss),
		LostAccusationFees:     c.params.ConvertValue(c.LostAccusationFees),
		LostAccusationRewards:  c.params.ConvertValue(c.LostAccusationRewards),
		LostAccusationDeposits: c.params.ConvertValue(c.LostAccusationDeposits),
		LostSeedFees:           c.params.ConvertValue(c.LostSeedFees),
		LostSeedRewards:        c.params.ConvertValue(c.LostSeedRewards),
		StartTime:              c.ctx.Indexer.LookupBlockTime(c.ctx.Context, c.params.CycleStartHeight(c.Cycle)),
		EndTime:                c.ctx.Indexer.LookupBlockTime(c.ctx.Context, c.params.CycleEndHeight(c.Cycle)),
	}
	return json.Marshal(inc)
}

func (c *Income) MarshalJSONBrief() ([]byte, error) {
	dec := c.params.Decimals
	buf := make([]byte, 0, 2048)
	buf = append(buf, '[')
	for i, v := range c.columns {
		switch v {
		case "row_id":
			buf = strconv.AppendUint(buf, c.RowId, 10)
		case "cycle":
			buf = strconv.AppendInt(buf, c.Cycle, 10)
		case "account_id":
			buf = strconv.AppendUint(buf, c.AccountId.Value(), 10)
		case "address":
			buf = strconv.AppendQuote(buf, c.ctx.Indexer.LookupAddress(c.ctx, c.AccountId).String())
		case "rolls":
			buf = strconv.AppendInt(buf, c.Rolls, 10)
		case "balance":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.Balance), 'f', dec, 64)
		case "delegated":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.Delegated), 'f', dec, 64)
		case "active_stake":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.ActiveStake), 'f', dec, 64)
		case "n_delegations":
			buf = strconv.AppendInt(buf, c.NDelegations, 10)
		case "n_baking_rights":
			buf = strconv.AppendInt(buf, c.NBakingRights, 10)
		case "n_endorsing_rights":
			buf = strconv.AppendInt(buf, c.NEndorsingRights, 10)
		case "luck":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.Luck), 'f', dec, 64)
		case "luck_percent":
			buf = strconv.AppendFloat(buf, float64(c.LuckPct)/100, 'f', 2, 64)
		case "contribution_percent":
			buf = strconv.AppendFloat(buf, float64(c.ContributionPct)/100, 'f', 2, 64)
		case "performance_percent":
			buf = strconv.AppendFloat(buf, float64(c.PerformancePct)/100, 'f', 2, 64)
		case "n_blocks_baked":
			buf = strconv.AppendInt(buf, c.NBlocksBaked, 10)
		case "n_blocks_proposed":
			buf = strconv.AppendInt(buf, c.NBlocksProposed, 10)
		case "n_blocks_not_baked":
			buf = strconv.AppendInt(buf, c.NBlocksNotBaked, 10)
		case "n_blocks_endorsed":
			buf = strconv.AppendInt(buf, c.NBlocksEndorsed, 10)
		case "n_blocks_not_endorsed":
			buf = strconv.AppendInt(buf, c.NBlocksNotEndorsed, 10)
		case "n_slots_endorsed":
			buf = strconv.AppendInt(buf, c.NSlotsEndorsed, 10)
		case "n_seeds_revealed":
			buf = strconv.AppendInt(buf, c.NSeedsRevealed, 10)
		case "expected_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.ExpectedIncome), 'f', dec, 64)
		case "total_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.TotalIncome), 'f', dec, 64)
		case "total_deposits":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.TotalDeposits), 'f', dec, 64)
		case "baking_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.BakingIncome), 'f', dec, 64)
		case "endorsing_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.EndorsingIncome), 'f', dec, 64)
		case "accusation_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.AccusationIncome), 'f', dec, 64)
		case "seed_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.SeedIncome), 'f', dec, 64)
		case "fees_income":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.FeesIncome), 'f', dec, 64)
		case "total_loss":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.TotalLoss), 'f', dec, 64)
		case "accusation_loss":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.AccusationLoss), 'f', dec, 64)
		case "seed_loss":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.SeedLoss), 'f', dec, 64)
		case "endorsing_loss":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.EndorsingLoss), 'f', dec, 64)
		case "lost_accusation_fees":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.LostAccusationFees), 'f', dec, 64)
		case "lost_accusation_rewards":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.LostAccusationRewards), 'f', dec, 64)
		case "lost_accusation_deposits":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.LostAccusationDeposits), 'f', dec, 64)
		case "lost_seed_fees":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.LostSeedFees), 'f', dec, 64)
		case "lost_seed_rewards":
			buf = strconv.AppendFloat(buf, c.params.ConvertValue(c.LostSeedRewards), 'f', dec, 64)
		case "start_time":
			buf = strconv.AppendInt(buf, c.ctx.Indexer.LookupBlockTimeMs(c.ctx.Context, c.params.CycleStartHeight(c.Cycle)), 10)
		case "end_time":
			buf = strconv.AppendInt(buf, c.ctx.Indexer.LookupBlockTimeMs(c.ctx.Context, c.params.CycleEndHeight(c.Cycle)), 10)
		default:
			continue
		}
		if i < len(c.columns)-1 {
			buf = append(buf, ',')
		}
	}
	buf = append(buf, ']')
	return buf, nil
}

func (c *Income) MarshalCSV() ([]string, error) {
	dec := c.params.Decimals
	res := make([]string, len(c.columns))
	for i, v := range c.columns {
		switch v {
		case "row_id":
			res[i] = strconv.FormatUint(c.RowId, 10)
		case "cycle":
			res[i] = strconv.FormatInt(c.Cycle, 10)
		case "account_id":
			res[i] = strconv.FormatUint(c.AccountId.Value(), 10)
		case "address":
			res[i] = strconv.Quote(c.ctx.Indexer.LookupAddress(c.ctx, c.AccountId).String())
		case "rolls":
			res[i] = strconv.FormatInt(c.Rolls, 10)
		case "balance":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.Balance), 'f', dec, 64)
		case "delegated":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.Delegated), 'f', dec, 64)
		case "active_stake":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.ActiveStake), 'f', dec, 64)
		case "n_delegations":
			res[i] = strconv.FormatInt(c.NDelegations, 10)
		case "n_baking_rights":
			res[i] = strconv.FormatInt(c.NBakingRights, 10)
		case "n_endorsing_rights":
			res[i] = strconv.FormatInt(c.NEndorsingRights, 10)
		case "luck":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.Luck), 'f', dec, 64)
		case "luck_percent":
			res[i] = strconv.FormatFloat(float64(c.LuckPct)/100, 'f', 2, 64)
		case "contribution_percent":
			res[i] = strconv.FormatFloat(float64(c.ContributionPct)/100, 'f', 2, 64)
		case "performance_percent":
			res[i] = strconv.FormatFloat(float64(c.PerformancePct)/100, 'f', 2, 64)
		case "n_blocks_baked":
			res[i] = strconv.FormatInt(c.NBlocksBaked, 10)
		case "n_blocks_proposed":
			res[i] = strconv.FormatInt(c.NBlocksProposed, 10)
		case "n_blocks_not_baked":
			res[i] = strconv.FormatInt(c.NBlocksNotBaked, 10)
		case "n_blocks_endorsed":
			res[i] = strconv.FormatInt(c.NBlocksEndorsed, 10)
		case "n_blocks_not_endorsed":
			res[i] = strconv.FormatInt(c.NBlocksNotEndorsed, 10)
		case "n_slots_endorsed":
			res[i] = strconv.FormatInt(c.NSlotsEndorsed, 10)
		case "n_seeds_revealed":
			res[i] = strconv.FormatInt(c.NSeedsRevealed, 10)
		case "expected_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.ExpectedIncome), 'f', dec, 64)
		case "total_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.TotalIncome), 'f', dec, 64)
		case "total_deposits":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.TotalDeposits), 'f', dec, 64)
		case "baking_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.BakingIncome), 'f', dec, 64)
		case "endorsing_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.EndorsingIncome), 'f', dec, 64)
		case "accusation_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.AccusationIncome), 'f', dec, 64)
		case "seed_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.SeedIncome), 'f', dec, 64)
		case "fees_income":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.FeesIncome), 'f', dec, 64)
		case "total_loss":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.TotalLoss), 'f', dec, 64)
		case "accusation_loss":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.AccusationLoss), 'f', dec, 64)
		case "seed_loss":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.SeedLoss), 'f', dec, 64)
		case "endorsing_loss":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.EndorsingLoss), 'f', dec, 64)
		case "lost_accusation_fees":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.LostAccusationFees), 'f', dec, 64)
		case "lost_accusation_rewards":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.LostAccusationRewards), 'f', dec, 64)
		case "lost_accusation_deposits":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.LostAccusationDeposits), 'f', dec, 64)
		case "lost_seed_fees":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.LostSeedFees), 'f', dec, 64)
		case "lost_seed_rewards":
			res[i] = strconv.FormatFloat(c.params.ConvertValue(c.LostSeedRewards), 'f', dec, 64)
		case "start_time":
			res[i] = strconv.Quote(c.ctx.Indexer.LookupBlockTime(c.ctx.Context, c.params.CycleStartHeight(c.Cycle)).Format(time.RFC3339))
		case "end_time":
			res[i] = strconv.Quote(c.ctx.Indexer.LookupBlockTime(c.ctx.Context, c.params.CycleEndHeight(c.Cycle)).Format(time.RFC3339))
		default:
			continue
		}
	}
	return res, nil
}

func StreamIncomeTable(ctx *server.Context, args *TableRequest) (interface{}, int) {
	// use chain params at current height
	params := ctx.Params

	// access table
	table, err := ctx.Indexer.Table(args.Table)
	if err != nil {
		panic(server.ENotFound(server.EC_RESOURCE_NOTFOUND, fmt.Sprintf("cannot access table '%s'", args.Table), err))
	}

	// translate long column names to short names used in pack tables
	var srcNames []string
	if len(args.Columns) > 0 {
		// resolve short column names
		srcNames = make([]string, 0, len(args.Columns))
		for _, v := range args.Columns {
			n, ok := incomeSourceNames[v]
			if !ok {
				panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("unknown column '%s'", v), nil))
			}
			if n != "-" {
				srcNames = append(srcNames, n)
			}
		}
	} else {
		// use all table columns in order and reverse lookup their long names
		srcNames = table.Fields().Names()
		args.Columns = incomeAllAliases
	}

	// build table query
	q := pack.NewQuery(ctx.RequestID, table).
		WithFields(srcNames...).
		WithLimit(int(args.Limit)).
		WithOrder(args.Order)

	// build dynamic filter conditions from query (will panic on error)
	for key, val := range ctx.Request.URL.Query() {
		keys := strings.Split(key, ".")
		prefix := keys[0]
		mode := pack.FilterModeEqual
		field := incomeSourceNames[prefix]
		if len(keys) > 1 {
			mode = pack.ParseFilterMode(keys[1])
			if !mode.IsValid() {
				panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode '%s'", keys[1]), nil))
			}
		}
		switch prefix {
		case "columns", "limit", "order", "verbose", "filename":
			// skip these fields
		case "cursor":
			// add row id condition: id > cursor (new cursor == last row id)
			id, err := strconv.ParseUint(val[0], 10, 64)
			if err != nil {
				panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid cursor value '%s'", val), err))
			}
			cursorMode := pack.FilterModeGt
			if args.Order == pack.OrderDesc {
				cursorMode = pack.FilterModeLt
			}
			q.Conditions.AddAndCondition(&pack.Condition{
				Field: table.Fields().Pk(),
				Mode:  cursorMode,
				Value: id,
				Raw:   val[0], // debugging aid
			})
		case "address":
			// parse address and lookup id
			// valid filter modes: eq, in
			// 1 resolve account_id from account table
			// 2 add eq/in cond: account_id
			// 3 cache result in map (for output)
			switch mode {
			case pack.FilterModeEqual, pack.FilterModeNotEqual:
				// single-address lookup and compile condition
				addr, err := tezos.ParseAddress(val[0])
				if err != nil || !addr.IsValid() {
					panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid address '%s'", val[0]), err))
				}
				acc, err := ctx.Indexer.LookupAccount(ctx, addr)
				if err != nil && err != index.ErrNoAccountEntry {
					panic(err)
				}
				// Note: when not found we insert an always false condition
				if acc == nil || acc.RowId == 0 {
					q.Conditions.AddAndCondition(&pack.Condition{
						Field: table.Fields().Find("A"), // account id
						Mode:  mode,
						Value: uint64(math.MaxUint64),
						Raw:   "account not found", // debugging aid
					})
				} else {
					// add addr id as extra fund_flow condition
					q.Conditions.AddAndCondition(&pack.Condition{
						Field: table.Fields().Find("A"), // account id
						Mode:  mode,
						Value: acc.RowId,
						Raw:   val[0], // debugging aid
					})
				}
			case pack.FilterModeIn, pack.FilterModeNotIn:
				// multi-address lookup and compile condition
				ids := make([]uint64, 0)
				for _, v := range strings.Split(val[0], ",") {
					addr, err := tezos.ParseAddress(v)
					if err != nil || !addr.IsValid() {
						panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid address '%s'", v), err))
					}
					acc, err := ctx.Indexer.LookupAccount(ctx, addr)
					if err != nil && err != index.ErrNoAccountEntry {
						panic(err)
					}
					// skip not found account
					if acc == nil || acc.RowId == 0 {
						continue
					}
					// collect list of account ids
					ids = append(ids, acc.RowId.Value())
				}
				// Note: when list is empty (no accounts were found, the match will
				//       always be false and return no result as expected)
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find("A"), // account id
					Mode:  mode,
					Value: ids,
					Raw:   val[0], // debugging aid
				})
			default:
				panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode '%s' for column '%s'", mode, prefix), nil))
			}
		case "time":
			// translate time into height, use val[0] only
			bestTime := ctx.Tip.BestTime
			bestHeight := ctx.Tip.BestHeight
			cond, err := pack.ParseCondition(key, val[0], pack.FieldList{
				pack.Field{
					Name: prefix,
					Type: pack.FieldTypeDatetime,
				},
			})
			if err != nil {
				panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s': %v", key, val[0], err), err))
			}
			// re-use the block height -> time slice because it's already loaded
			// into memory, the binary search should be faster than a block query
			switch cond.Mode {
			case pack.FilterModeRange:
				// use cond.From and con.To
				from, to := cond.From.(time.Time), cond.To.(time.Time)
				var fromBlock, toBlock int64
				if !from.After(bestTime) {
					fromBlock = ctx.Indexer.LookupBlockHeightFromTime(ctx.Context, from)
				} else {
					nDiff := int64(from.Sub(bestTime) / params.BlockTime())
					fromBlock = bestHeight + nDiff
				}
				if !to.After(bestTime) {
					toBlock = ctx.Indexer.LookupBlockHeightFromTime(ctx.Context, to)
				} else {
					nDiff := int64(to.Sub(bestTime) / params.BlockTime())
					toBlock = bestHeight + nDiff
				}
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find(field),
					Mode:  cond.Mode,
					From:  params.CycleFromHeight(fromBlock),
					To:    params.CycleFromHeight(toBlock),
					Raw:   val[0], // debugging aid
				})
			default:
				// cond.Value is time.Time
				valueTime := cond.Value.(time.Time)
				var valueCycle int64
				if !valueTime.After(bestTime) {
					height := ctx.Indexer.LookupBlockHeightFromTime(ctx.Context, valueTime)
					valueCycle = params.CycleFromHeight(height)
				} else {
					nDiff := int64(valueTime.Sub(bestTime) / params.BlockTime())
					valueCycle = params.CycleFromHeight(bestHeight + nDiff)
				}
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find(field),
					Mode:  cond.Mode,
					Value: valueCycle,
					Raw:   val[0], // debugging aid
				})
			}

		default:
			// translate long column name used in query to short column name used in packs
			if short, ok := incomeSourceNames[prefix]; !ok {
				panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("unknown column '%s'", prefix), nil))
			} else {
				key = strings.Replace(key, prefix, short, 1)
			}

			// the same field name may appear multiple times, in which case conditions
			// are combined like any other condition with logical AND
			for _, v := range val {
				// convert amounts from float to int64
				switch prefix {
				case "cycle":
					if v == "head" {
						currentCycle := params.CycleFromHeight(ctx.Tip.BestHeight)
						v = strconv.FormatInt(int64(currentCycle), 10)
					}

				case "start_time", "end_time":
					// convert time -> block -> cycle
					if mode != pack.FilterModeEqual {
						panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode for column '%s'", prefix), nil))
					}
					tm, err := util.ParseTime(v)
					if err != nil {
						panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s'", prefix, v), err))
					}
					cmode := pack.FilterModeLte
					if prefix == "start_time" {
						cmode = pack.FilterModeGte
					}
					q.Conditions.AddAndCondition(&pack.Condition{
						Field: table.Fields().Find("c"), // cycle
						Mode:  cmode,
						Value: params.CycleFromHeight(ctx.Indexer.LookupBlockHeightFromTime(ctx.Context, tm.Time())),
						Raw:   v,
					})
					// skip further parsing
					continue

				case "luck_percent", "contribution_percent", "performance_percent":
					fvals := make([]string, 0)
					for _, vv := range strings.Split(v, ",") {
						fval, err := strconv.ParseFloat(vv, 64)
						if err != nil {
							panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s'", key, vv), err))
						}
						fvals = append(fvals, strconv.FormatInt(int64(fval*10000), 10))
					}
					v = strings.Join(fvals, ",")

				case "luck", "balance", "delegated", "active_stake", "expected_income",
					"total_income", "total_bonds", "baking_income", "endorsing_income",
					"accusation_income", "seed_income", "fees_income",
					"total_loss", "accusation_loss", "seed_loss", "endorsing_loss",
					"lost_accusation_fees", "lost_accusation_rewards", "lost_accusation_deposits",
					"lost_seed_fees", "lost_seed_rewards":
					fvals := make([]string, 0)
					for _, vv := range strings.Split(v, ",") {
						fval, err := strconv.ParseFloat(vv, 64)
						if err != nil {
							panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s'", key, vv), err))
						}
						fvals = append(fvals, strconv.FormatInt(params.ConvertAmount(fval), 10))
					}
					v = strings.Join(fvals, ",")
				}
				if cond, err := pack.ParseCondition(key, v, table.Fields()); err != nil {
					panic(server.EBadRequest(server.EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s'", key, v), err))
				} else {
					q.Conditions.AddAndCondition(&cond)
				}
			}
		}
	}

	var (
		count  int
		lastId uint64
	)

	// start := time.Now()
	// ctx.Log.Tracef("Streaming max %d rows from %s", args.Limit, args.Table)
	// defer func() {
	// 	ctx.Log.Tracef("Streamed %d rows in %s", count, time.Since(start))
	// }()

	// prepare return type marshalling
	inc := &Income{
		verbose: args.Verbose,
		columns: util.StringList(args.Columns),
		params:  params,
		ctx:     ctx,
	}

	// prepare response stream
	ctx.StreamResponseHeaders(http.StatusOK, mimetypes[args.Format])

	switch args.Format {
	case "json":
		enc := json.NewEncoder(ctx.ResponseWriter)
		enc.SetIndent("", "")
		enc.SetEscapeHTML(false)

		// open JSON array
		io.WriteString(ctx.ResponseWriter, "[")
		// close JSON array on panic
		defer func() {
			if e := recover(); e != nil {
				io.WriteString(ctx.ResponseWriter, "]")
				panic(e)
			}
		}()

		// run query and stream results
		var needComma bool
		err = table.Stream(ctx, q, func(r pack.Row) error {
			if needComma {
				io.WriteString(ctx.ResponseWriter, ",")
			} else {
				needComma = true
			}
			if err := r.Decode(inc); err != nil {
				return err
			}
			if err := enc.Encode(inc); err != nil {
				return err
			}
			count++
			lastId = inc.RowId
			if args.Limit > 0 && count == int(args.Limit) {
				return io.EOF
			}
			return nil
		})
		// close JSON bracket
		io.WriteString(ctx.ResponseWriter, "]")
		// ctx.Log.Tracef("JSON encoded %d rows", count)

	case "csv":
		enc := csv.NewEncoder(ctx.ResponseWriter)
		// use custom header columns and order
		if len(args.Columns) > 0 {
			err = enc.EncodeHeader(args.Columns, nil)
		}
		if err == nil {
			// run query and stream results
			err = table.Stream(ctx, q, func(r pack.Row) error {
				if err := r.Decode(inc); err != nil {
					return err
				}
				if err := enc.EncodeRecord(inc); err != nil {
					return err
				}
				count++
				lastId = inc.RowId
				if args.Limit > 0 && count == int(args.Limit) {
					return io.EOF
				}
				return nil
			})
		}
		// ctx.Log.Tracef("CSV Encoded %d rows", count)
	}

	// without new records, cursor remains the same as input (may be empty)
	cursor := args.Cursor
	if lastId > 0 {
		cursor = strconv.FormatUint(lastId, 10)
	}

	// write error (except EOF), cursor and count as http trailer
	ctx.StreamTrailer(cursor, count, err)

	// streaming return
	return nil, -1
}
