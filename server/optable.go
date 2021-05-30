// Copyright (c) 2020-2021 Blockwatch Data Inc.
// Author: alex@blockwatch.cc

package server

import (
	"encoding/hex"
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
)

var (
	// long -> short form
	opSourceNames map[string]string
	// short -> long form
	opAliasNames map[string]string
	// all aliases as list
	opAllAliases []string
)

func init() {
	fields, err := pack.Fields(&model.Op{})
	if err != nil {
		log.Fatalf("op field type error: %v\n", err)
	}
	opSourceNames = fields.NameMapReverse()
	opAllAliases = fields.Aliases()

	// add extra translations for related accounts
	opSourceNames["sender"] = "S"
	opSourceNames["receiver"] = "R"
	opSourceNames["creator"] = "M"
	opSourceNames["delegate"] = "D"
	opSourceNames["branch_id"] = "-"
	opAllAliases = append(opAllAliases, "sender")
	opAllAliases = append(opAllAliases, "receiver")
	opAllAliases = append(opAllAliases, "creator")
	opAllAliases = append(opAllAliases, "delegate")
}

// configurable marshalling helper
type Op struct {
	model.Op
	verbose bool            // cond. marshal
	columns util.StringList // cond. cols & order when brief
	params  *tezos.Params   // blockchain amount conversion
	ctx     *ApiContext
}

func (o *Op) MarshalJSON() ([]byte, error) {
	if o.verbose {
		return o.MarshalJSONVerbose()
	} else {
		return o.MarshalJSONBrief()
	}
}

func (o *Op) MarshalJSONVerbose() ([]byte, error) {
	op := struct {
		RowId        uint64          `json:"row_id"`
		Timestamp    int64           `json:"time"`
		Height       int64           `json:"height"`
		Cycle        int64           `json:"cycle"`
		Hash         string          `json:"hash"`
		Counter      int64           `json:"counter"`
		OpN          int             `json:"op_n"`
		OpL          int             `json:"op_l"`
		OpP          int             `json:"op_p"`
		OpC          int             `json:"op_c"`
		OpI          int             `json:"op_i"`
		Type         string          `json:"type"`
		Status       string          `json:"status"`
		GasLimit     int64           `json:"gas_limit"`
		GasUsed      int64           `json:"gas_used"`
		GasPrice     float64         `json:"gas_price"`
		StorageLimit int64           `json:"storage_limit"`
		StorageSize  int64           `json:"storage_size"`
		StoragePaid  int64           `json:"storage_paid"`
		Volume       float64         `json:"volume"`
		Fee          float64         `json:"fee"`
		Reward       float64         `json:"reward"`
		Deposit      float64         `json:"deposit"`
		Burned       float64         `json:"burned"`
		SenderId     uint64          `json:"sender_id"`
		Sender       string          `json:"sender"`
		ReceiverId   uint64          `json:"receiver_id"`
		Receiver     string          `json:"receiver"`
		CreatorId    uint64          `json:"creator_id"`
		Creator      string          `json:"creator"`
		DelegateId   uint64          `json:"delegate_id"`
		Delegate     string          `json:"delegate"`
		IsSuccess    bool            `json:"is_success"`
		IsContract   bool            `json:"is_contract"`
		IsInternal   bool            `json:"is_internal"`
		HasData      bool            `json:"has_data"`
		Data         string          `json:"data,omitempty"`
		Parameters   string          `json:"parameters,omitempty"`
		Storage      string          `json:"storage,omitempty"`
		BigmapDiff   string          `json:"big_map_diff,omitempty"`
		Errors       json.RawMessage `json:"errors,omitempty"`
		TDD          float64         `json:"days_destroyed"`
		BranchHash   string          `json:"branch_hash"`
		BranchHeight int64           `json:"branch_height"`
		BranchDepth  int64           `json:"branch_depth"`
		IsImplicit   bool            `json:"is_implicit"`
		Entrypoint   int             `json:"entrypoint_id"`
		IsOrphan     bool            `json:"is_orphan"`
		IsBatch      bool            `json:"is_batch"`
		IsSapling    bool            `json:"is_sapling"`
	}{
		RowId:        o.RowId.Value(),
		Timestamp:    util.UnixMilliNonZero(o.Timestamp),
		Height:       o.Height,
		Cycle:        o.Cycle,
		Counter:      o.Counter,
		OpN:          o.OpN,
		OpL:          o.OpL,
		OpP:          o.OpP,
		OpC:          o.OpC,
		OpI:          o.OpI,
		Type:         o.Type.String(),
		Status:       o.Status.String(),
		GasLimit:     o.GasLimit,
		GasUsed:      o.GasUsed,
		GasPrice:     o.GasPrice,
		StorageLimit: o.StorageLimit,
		StorageSize:  o.StorageSize,
		StoragePaid:  o.StoragePaid,
		Volume:       o.params.ConvertValue(o.Volume),
		Fee:          o.params.ConvertValue(o.Fee),
		Reward:       o.params.ConvertValue(o.Reward),
		Deposit:      o.params.ConvertValue(o.Deposit),
		Burned:       o.params.ConvertValue(o.Burned),
		SenderId:     o.SenderId.Value(),
		Sender:       o.ctx.Indexer.LookupAddress(o.ctx, o.SenderId).String(),
		ReceiverId:   o.ReceiverId.Value(),
		Receiver:     o.ctx.Indexer.LookupAddress(o.ctx, o.ReceiverId).String(),
		CreatorId:    o.CreatorId.Value(),
		Creator:      o.ctx.Indexer.LookupAddress(o.ctx, o.CreatorId).String(),
		DelegateId:   o.DelegateId.Value(),
		Delegate:     o.ctx.Indexer.LookupAddress(o.ctx, o.DelegateId).String(),
		IsSuccess:    o.IsSuccess,
		IsContract:   o.IsContract,
		IsInternal:   o.IsInternal,
		HasData:      o.HasData,
		Data:         o.Data,
		Parameters:   "",
		Storage:      "",
		BigmapDiff:   "",
		Errors:       nil,
		TDD:          o.TDD,
		BranchHeight: o.BranchHeight,
		BranchDepth:  o.BranchDepth,
		IsImplicit:   o.IsImplicit,
		Entrypoint:   o.Entrypoint,
		IsOrphan:     o.IsOrphan,
		IsBatch:      o.IsBatch,
		IsSapling:    o.IsSapling,
	}

	if o.BranchId != 0 {
		if h, err := o.ctx.Indexer.BlockHashById(o.ctx.Context, o.BranchId); err == nil {
			op.BranchHash = h.String()
		}
	}
	if !o.Hash.Equal(tezos.ZeroOpHash) {
		op.Hash = o.Hash.String()
	}
	if len(o.Parameters) > 0 {
		op.Parameters = hex.EncodeToString(o.Parameters)
	}
	if len(o.Storage) > 0 {
		op.Storage = hex.EncodeToString(o.Storage)
	}
	if len(o.BigmapDiff) > 0 {
		op.BigmapDiff = hex.EncodeToString(o.BigmapDiff)
	}
	if o.Errors != "" {
		op.Errors = json.RawMessage(o.Errors)
	}

	return json.Marshal(op)
}

func (o *Op) MarshalJSONBrief() ([]byte, error) {
	dec := o.params.Decimals
	buf := make([]byte, 0, 2048)
	buf = append(buf, '[')
	for i, v := range o.columns {
		switch v {
		case "row_id":
			buf = strconv.AppendUint(buf, o.RowId.Value(), 10)
		case "time":
			buf = strconv.AppendInt(buf, util.UnixMilliNonZero(o.Timestamp), 10)
		case "height":
			buf = strconv.AppendInt(buf, o.Height, 10)
		case "cycle":
			buf = strconv.AppendInt(buf, o.Cycle, 10)
		case "hash":
			if !o.Hash.Equal(tezos.ZeroOpHash) {
				buf = strconv.AppendQuote(buf, o.Hash.String())
			} else {
				buf = append(buf, []byte(`""`)...)
			}
		case "counter":
			buf = strconv.AppendInt(buf, o.Counter, 10)
		case "op_n":
			buf = strconv.AppendInt(buf, int64(o.OpN), 10)
		case "op_l":
			buf = strconv.AppendInt(buf, int64(o.OpL), 10)
		case "op_p":
			buf = strconv.AppendInt(buf, int64(o.OpP), 10)
		case "op_c":
			buf = strconv.AppendInt(buf, int64(o.OpC), 10)
		case "op_i":
			buf = strconv.AppendInt(buf, int64(o.OpI), 10)
		case "type":
			buf = strconv.AppendQuote(buf, o.Type.String())
		case "status":
			buf = strconv.AppendQuote(buf, o.Status.String())
		case "gas_limit":
			buf = strconv.AppendInt(buf, o.GasLimit, 10)
		case "gas_used":
			buf = strconv.AppendInt(buf, o.GasUsed, 10)
		case "gas_price":
			buf = strconv.AppendFloat(buf, o.GasPrice, 'f', 3, 64)
		case "storage_limit":
			buf = strconv.AppendInt(buf, o.StorageLimit, 10)
		case "storage_size":
			buf = strconv.AppendInt(buf, o.StorageSize, 10)
		case "storage_paid":
			buf = strconv.AppendInt(buf, o.StoragePaid, 10)
		case "volume":
			buf = strconv.AppendFloat(buf, o.params.ConvertValue(o.Volume), 'f', dec, 64)
		case "fee":
			buf = strconv.AppendFloat(buf, o.params.ConvertValue(o.Fee), 'f', dec, 64)
		case "reward":
			buf = strconv.AppendFloat(buf, o.params.ConvertValue(o.Reward), 'f', dec, 64)
		case "deposit":
			buf = strconv.AppendFloat(buf, o.params.ConvertValue(o.Deposit), 'f', dec, 64)
		case "burned":
			buf = strconv.AppendFloat(buf, o.params.ConvertValue(o.Burned), 'f', dec, 64)
		case "sender_id":
			buf = strconv.AppendUint(buf, o.SenderId.Value(), 10)
		case "sender":
			buf = strconv.AppendQuote(buf, o.ctx.Indexer.LookupAddress(o.ctx, o.SenderId).String())
		case "receiver_id":
			buf = strconv.AppendUint(buf, o.ReceiverId.Value(), 10)
		case "receiver":
			if o.ReceiverId > 0 {
				buf = strconv.AppendQuote(buf, o.ctx.Indexer.LookupAddress(o.ctx, o.ReceiverId).String())
			} else {
				buf = append(buf, "null"...)
			}
		case "creator_id":
			buf = strconv.AppendUint(buf, o.CreatorId.Value(), 10)
		case "creator":
			if o.CreatorId > 0 {
				buf = strconv.AppendQuote(buf, o.ctx.Indexer.LookupAddress(o.ctx, o.CreatorId).String())
			} else {
				buf = append(buf, "null"...)
			}
		case "delegate_id":
			buf = strconv.AppendUint(buf, o.DelegateId.Value(), 10)
		case "delegate":
			if o.DelegateId > 0 {
				buf = strconv.AppendQuote(buf, o.ctx.Indexer.LookupAddress(o.ctx, o.DelegateId).String())
			} else {
				buf = append(buf, "null"...)
			}
		case "is_success":
			if o.IsSuccess {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "is_contract":
			if o.IsContract {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "is_internal":
			if o.IsInternal {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "has_data":
			if o.HasData {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "data":
			if o.Data != "" {
				buf = strconv.AppendQuote(buf, o.Data)
			} else {
				buf = append(buf, "null"...)
			}
		case "parameters":
			// parameters is binary
			if len(o.Parameters) > 0 {
				buf = strconv.AppendQuote(buf, hex.EncodeToString(o.Parameters))
			} else {
				buf = append(buf, "null"...)
			}
		case "storage":
			// storage is binary
			if len(o.Storage) > 0 {
				buf = strconv.AppendQuote(buf, hex.EncodeToString(o.Storage))
			} else {
				buf = append(buf, "null"...)
			}
		case "big_map_diff":
			// big_map_diff is binary
			if len(o.BigmapDiff) > 0 {
				buf = strconv.AppendQuote(buf, hex.EncodeToString(o.BigmapDiff))
			} else {
				buf = append(buf, "null"...)
			}
		case "errors":
			// errors is json
			if o.Errors != "" {
				buf = append(buf, o.Errors...)
			} else {
				buf = append(buf, "null"...)
			}
		case "days_destroyed":
			buf = strconv.AppendFloat(buf, o.TDD, 'f', -1, 64)
		case "branch_height":
			buf = strconv.AppendInt(buf, o.BranchHeight, 10)
		case "branch_depth":
			buf = strconv.AppendInt(buf, o.BranchDepth, 10)
		case "branch_hash":
			ok := false
			if o.BranchId != 0 {
				if h, err := o.ctx.Indexer.BlockHashById(o.ctx.Context, o.BranchId); err == nil {
					buf = strconv.AppendQuote(buf, h.String())
					ok = true
				}
			}
			if !ok {
				buf = append(buf, []byte(`""`)...)
			}
		case "is_implicit":
			if o.IsImplicit {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "entrypoint_id":
			buf = strconv.AppendInt(buf, int64(o.Entrypoint), 10)
		case "is_orphan":
			if o.IsOrphan {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "is_batch":
			if o.IsBatch {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		case "is_sapling":
			if o.IsSapling {
				buf = append(buf, '1')
			} else {
				buf = append(buf, '0')
			}
		default:
			continue
		}
		if i < len(o.columns)-1 {
			buf = append(buf, ',')
		}
	}
	buf = append(buf, ']')
	return buf, nil
}

func (o *Op) MarshalCSV() ([]string, error) {
	dec := o.params.Decimals
	res := make([]string, len(o.columns))
	for i, v := range o.columns {
		switch v {
		case "row_id":
			res[i] = strconv.FormatUint(o.RowId.Value(), 10)
		case "time":
			res[i] = strconv.Quote(o.Timestamp.Format(time.RFC3339))
		case "height":
			res[i] = strconv.FormatInt(o.Height, 10)
		case "cycle":
			res[i] = strconv.FormatInt(o.Cycle, 10)
		case "hash":
			if !o.Hash.Equal(tezos.ZeroOpHash) {
				res[i] = strconv.Quote(o.Hash.String())
			} else {
				res[i] = `""`
			}
		case "counter":
			res[i] = strconv.FormatInt(o.Counter, 10)
		case "op_n":
			res[i] = strconv.FormatInt(int64(o.OpN), 10)
		case "op_l":
			res[i] = strconv.FormatInt(int64(o.OpL), 10)
		case "op_p":
			res[i] = strconv.FormatInt(int64(o.OpP), 10)
		case "op_c":
			res[i] = strconv.FormatInt(int64(o.OpC), 10)
		case "op_i":
			res[i] = strconv.FormatInt(int64(o.OpI), 10)
		case "type":
			res[i] = strconv.Quote(o.Type.String())
		case "status":
			res[i] = strconv.Quote(o.Status.String())
		case "gas_limit":
			res[i] = strconv.FormatInt(o.GasLimit, 10)
		case "gas_used":
			res[i] = strconv.FormatInt(o.GasUsed, 10)
		case "gas_price":
			res[i] = strconv.FormatFloat(o.GasPrice, 'f', 3, 64)
		case "storage_limit":
			res[i] = strconv.FormatInt(o.StorageLimit, 10)
		case "storage_size":
			res[i] = strconv.FormatInt(o.StorageSize, 10)
		case "storage_paid":
			res[i] = strconv.FormatInt(o.StoragePaid, 10)
		case "volume":
			res[i] = strconv.FormatFloat(o.params.ConvertValue(o.Volume), 'f', dec, 64)
		case "fee":
			res[i] = strconv.FormatFloat(o.params.ConvertValue(o.Fee), 'f', dec, 64)
		case "reward":
			res[i] = strconv.FormatFloat(o.params.ConvertValue(o.Reward), 'f', dec, 64)
		case "deposit":
			res[i] = strconv.FormatFloat(o.params.ConvertValue(o.Deposit), 'f', dec, 64)
		case "burned":
			res[i] = strconv.FormatFloat(o.params.ConvertValue(o.Burned), 'f', dec, 64)
		case "sender_id":
			res[i] = strconv.FormatUint(o.SenderId.Value(), 10)
		case "sender":
			res[i] = strconv.Quote(o.ctx.Indexer.LookupAddress(o.ctx, o.SenderId).String())
		case "receiver_id":
			res[i] = strconv.FormatUint(o.ReceiverId.Value(), 10)
		case "receiver":
			res[i] = strconv.Quote(o.ctx.Indexer.LookupAddress(o.ctx, o.ReceiverId).String())
		case "creator_id":
			res[i] = strconv.FormatUint(o.CreatorId.Value(), 10)
		case "creator":
			res[i] = strconv.Quote(o.ctx.Indexer.LookupAddress(o.ctx, o.CreatorId).String())
		case "delegate_id":
			res[i] = strconv.FormatUint(o.DelegateId.Value(), 10)
		case "delegate":
			res[i] = strconv.Quote(o.ctx.Indexer.LookupAddress(o.ctx, o.DelegateId).String())
		case "is_success":
			res[i] = strconv.FormatBool(o.IsSuccess)
		case "is_contract":
			res[i] = strconv.FormatBool(o.IsContract)
		case "is_internal":
			res[i] = strconv.FormatBool(o.IsInternal)
		case "has_data":
			res[i] = strconv.FormatBool(o.HasData)
		case "data":
			res[i] = strconv.Quote(o.Data)
		case "parameters":
			res[i] = strconv.Quote(hex.EncodeToString(o.Parameters))
		case "storage":
			res[i] = strconv.Quote(hex.EncodeToString(o.Storage))
		case "big_map_diff":
			res[i] = strconv.Quote(hex.EncodeToString(o.BigmapDiff))
		case "errors":
			res[i] = strconv.Quote(o.Errors)
		case "days_destroyed":
			res[i] = strconv.FormatFloat(o.TDD, 'f', -1, 64)
		case "branch_height":
			res[i] = strconv.FormatInt(o.BranchHeight, 10)
		case "branch_depth":
			res[i] = strconv.FormatInt(o.BranchDepth, 10)
		case "branch_hash":
			ok := false
			if o.BranchId != 0 {
				if h, err := o.ctx.Indexer.BlockHashById(o.ctx.Context, o.BranchId); err == nil {
					res[i] = strconv.Quote(h.String())
					ok = true
				}
			}
			if !ok {
				res[i] = `""`
			}
		case "is_implicit":
			res[i] = strconv.FormatBool(o.IsImplicit)
		case "entrypoint_id":
			res[i] = strconv.FormatInt(int64(o.Entrypoint), 10)
		case "is_orphan":
			res[i] = strconv.FormatBool(o.IsOrphan)
		case "is_batch":
			res[i] = strconv.FormatBool(o.IsBatch)
		case "is_sapling":
			res[i] = strconv.FormatBool(o.IsSapling)
		default:
			continue
		}
	}
	return res, nil
}

func StreamOpTable(ctx *ApiContext, args *TableRequest) (interface{}, int) {
	// use chain params at current height
	params := ctx.Params

	// access table
	table, err := ctx.Indexer.Table(args.Table)
	if err != nil {
		panic(EConflict(EC_RESOURCE_STATE_UNEXPECTED, fmt.Sprintf("cannot access table '%s'", args.Table), err))
	}

	// translate long column names to short names used in pack tables
	// var needAccountT bool
	var srcNames []string
	if len(args.Columns) > 0 {
		// resolve short column names
		srcNames = make([]string, 0, len(args.Columns))
		for _, v := range args.Columns {
			n, ok := opSourceNames[v]
			if !ok {
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("unknown column '%s'", v), nil))
			}
			if n != "-" {
				srcNames = append(srcNames, n)
			}
			switch v {
			case "data":
				srcNames = append(srcNames, "has_data")
			}
		}
	} else {
		// use all table columns in order and reverse lookup their long names
		srcNames = table.Fields().Names()
		args.Columns = opAllAliases
	}

	// build table query
	q := pack.Query{
		Name:   ctx.RequestID,
		Fields: table.Fields().Select(srcNames...),
		Limit:  int(args.Limit),
		Order:  args.Order,
	}

	// build dynamic filter conditions from query (will panic on error)
	for key, val := range ctx.Request.URL.Query() {
		keys := strings.Split(key, ".")
		prefix := keys[0]
		mode := pack.FilterModeEqual
		if len(keys) > 1 {
			mode = pack.ParseFilterMode(keys[1])
			if !mode.IsValid() {
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode '%s'", keys[1]), nil))
			}
		}
		switch prefix {
		case "columns", "limit", "order", "verbose", "filename":
			// skip these fields
		case "cursor":
			// add row id condition: id > cursor (new cursor == last row id)
			id, err := strconv.ParseUint(val[0], 10, 64)
			if err != nil {
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid cursor value '%s'", val), err))
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
		case "hash":
			// special hash type to []byte conversion
			hashes := make([][]byte, len(val))
			for i, v := range val {
				h, err := tezos.ParseOpHash(v)
				if err != nil {
					panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid operation hash '%s'", val), err))
				}
				hashes[i] = h.Hash.Hash
			}
			q.Conditions.AddAndCondition(&pack.Condition{
				Field: table.Fields().Find("H"),
				Mode:  pack.FilterModeIn,
				Value: hashes,
				Raw:   strings.Join(val, ","), // debugging aid
			})
		case "type":
			// parse only the first value
			switch mode {
			case pack.FilterModeEqual, pack.FilterModeNotEqual:
				typ := tezos.ParseOpType(val[0])
				if !typ.IsValid() {
					panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid operation type '%s'", val[0]), nil))
				}
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find("t"),
					Mode:  mode,
					Value: typ,
					Raw:   val[0], // debugging aid
				})
			case pack.FilterModeIn, pack.FilterModeNotIn:
				typs := make([]uint8, 0)
				for _, t := range strings.Split(val[0], ",") {
					typ := tezos.ParseOpType(t)
					if !typ.IsValid() {
						panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid operation type '%s'", t), nil))
					}
					typs = append(typs, uint8(typ))
				}
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find("t"),
					Mode:  mode,
					Value: typs,
					Raw:   val[0], // debugging aid
				})

			default:
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode '%s' for column '%s'", mode, prefix), nil))
			}
		case "status":
			// parse only the first value
			switch mode {
			case pack.FilterModeEqual, pack.FilterModeNotEqual:
				stat := tezos.ParseOpStatus(val[0])
				if !stat.IsValid() {
					panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid operation status '%s'", val[0]), nil))
				}
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find("?"),
					Mode:  mode,
					Value: stat,
					Raw:   val[0], // debugging aid
				})
			case pack.FilterModeIn, pack.FilterModeNotIn:
				stats := make([]uint8, 0)
				for _, t := range strings.Split(val[0], ",") {
					stat := tezos.ParseOpStatus(t)
					if !stat.IsValid() {
						panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid operation status '%s'", t), nil))
					}
					stats = append(stats, uint8(stat))
				}
				q.Conditions.AddAndCondition(&pack.Condition{
					Field: table.Fields().Find("?"),
					Mode:  mode,
					Value: stats,
					Raw:   val[0], // debugging aid
				})

			default:
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode '%s' for column '%s'", mode, prefix), nil))
			}
		case "sender", "receiver", "creator", "delegate":
			// parse address and lookup id
			// valid filter modes: eq, in
			// 1 resolve account_id from account table
			// 2 add eq/in cond: account_id
			// 3 cache result in map (for output)
			field := opSourceNames[prefix]
			switch mode {
			case pack.FilterModeEqual, pack.FilterModeNotEqual:
				if val[0] == "" {
					// empty address matches id 0 (== missing baker)
					q.Conditions.AddAndCondition(&pack.Condition{
						Field: table.Fields().Find(field), // account id
						Mode:  pack.FilterModeEqual,
						Value: 0,
						Raw:   val[0], // debugging aid
					})
				} else {
					// single-address lookup and compile condition
					addr, err := tezos.ParseAddress(val[0])
					if err != nil {
						panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid address '%s'", val[0]), err))
					}
					acc, err := ctx.Indexer.LookupAccount(ctx, addr)
					if err != nil && err != index.ErrNoAccountEntry {
						panic(err)
					}
					// Note: when not found we insert an always false condition
					if acc == nil || acc.RowId == 0 {
						q.Conditions.AddAndCondition(&pack.Condition{
							Field: table.Fields().Find(field), // account id
							Mode:  mode,
							Value: uint64(math.MaxUint64),
							Raw:   "account not found", // debugging aid
						})
					} else {
						// add id as extra condition
						q.Conditions.AddAndCondition(&pack.Condition{
							Field: table.Fields().Find(field), // account id
							Mode:  mode,
							Value: acc.RowId,
							Raw:   val[0], // debugging aid
						})
					}
				}
			case pack.FilterModeIn, pack.FilterModeNotIn:
				// multi-address lookup and compile condition
				ids := make([]uint64, 0)
				for _, a := range strings.Split(val[0], ",") {
					addr, err := tezos.ParseAddress(a)
					if err != nil {
						panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid address '%s'", val[0]), err))
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
					Field: table.Fields().Find(field), // account id
					Mode:  mode,
					Value: ids,
					Raw:   val[0], // debugging aid
				})
			default:
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid filter mode '%s' for column '%s'", mode, prefix), nil))
			}
		default:
			// translate long column name used in query to short column name used in packs
			if short, ok := opSourceNames[prefix]; !ok {
				panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("unknown column '%s'", prefix), nil))
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
				case "volume", "reward", "fee", "deposit", "burned":
					fvals := make([]string, 0)
					for _, vv := range strings.Split(v, ",") {
						fval, err := strconv.ParseFloat(vv, 64)
						if err != nil {
							panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s'", key, vv), err))
						}
						fvals = append(fvals, strconv.FormatInt(params.ConvertAmount(fval), 10))
					}
					v = strings.Join(fvals, ",")
				}
				if cond, err := pack.ParseCondition(key, v, table.Fields()); err != nil {
					panic(EBadRequest(EC_PARAM_INVALID, fmt.Sprintf("invalid %s filter value '%s'", key, v), err))
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

	// Step 1: query database
	res, err := table.Query(ctx, q)
	if err != nil {
		panic(EInternal(EC_DATABASE, "query failed", err))
	}
	// ctx.Log.Tracef("Processing result with %d rows %d cols", res.Rows(), res.Cols())
	defer res.Close()

	// prepare return type marshalling
	op := &Op{
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
		err = res.Walk(func(r pack.Row) error {
			if needComma {
				io.WriteString(ctx.ResponseWriter, ",")
			} else {
				needComma = true
			}
			if err := r.Decode(op); err != nil {
				return err
			}
			if err := enc.Encode(op); err != nil {
				return err
			}
			count++
			lastId = op.RowId.Value()
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
			err = res.Walk(func(r pack.Row) error {
				if err := r.Decode(op); err != nil {
					return err
				}
				if err := enc.EncodeRecord(op); err != nil {
					return err
				}
				count++
				lastId = op.RowId.Value()
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
