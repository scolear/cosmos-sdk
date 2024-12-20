package abci

import (
	"cosmossdk.io/log"
	keeper2 "cosmossdk.io/x/symStaking/keeper"
	stakingtypes "cosmossdk.io/x/symStaking/types"
	"encoding/json"
	"errors"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum"
	"os"
)

type ProposalHandler struct {
	logger        log.Logger
	keeper        *keeper2.Keeper
	prevBlockTime uint64
}

func NewProposalHandler(logger log.Logger, keeper *keeper2.Keeper) *ProposalHandler {
	return &ProposalHandler{
		logger: logger,
		keeper: keeper,
	}
}

func (h *ProposalHandler) PrepareProposal() sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *abci.PrepareProposalRequest) (*abci.PrepareProposalResponse, error) {
		proposalTxs := req.Txs

		if req.Height%keeper2.SYMBIOTIC_SYNC_PERIOD != 0 {
			return &abci.PrepareProposalResponse{
				Txs: proposalTxs,
			}, nil
		}

		blockHash, err := h.keeper.GetFinalizedBlockHash(ctx)
		if err != nil {
			// anyway recovers in baseapp.abci so just skip
			blockHash = keeper2.INVALID_BLOCKHASH
		}

		// NOTE: We use stdlib JSON encoding, but an application may choose to use
		// a performant mechanism. This is for demo purposes only.
		bz, err := json.Marshal(blockHash)
		if err != nil {
			return nil, errors.New("failed to encode injected vote extension tx")
		}

		// Inject a "fake" tx into the proposal s.t. validators can decode, verify,
		// and store the canonical stake-weighted average prices.
		proposalTxs = append([][]byte{bz}, proposalTxs...)

		return &abci.PrepareProposalResponse{
			Txs: proposalTxs,
		}, nil
	}
}

func (h *ProposalHandler) PreBlocker() sdk.PreBlocker {
	return func(ctx sdk.Context, req *abci.FinalizeBlockRequest) error {
		if req.Height%keeper2.SYMBIOTIC_SYNC_PERIOD != 0 || len(req.Txs) == 0 {
			return nil
		}

		skipBlockHash := stakingtypes.CachedBlockHash{BlockHash: keeper2.INVALID_BLOCKHASH, Height: req.Height}

		var blockHash string
		if err := json.Unmarshal(req.Txs[0], &blockHash); err != nil {
			return err
		}

		if blockHash == keeper2.INVALID_BLOCKHASH {
			err := h.keeper.CacheBlockHash(ctx, skipBlockHash)
			return err
		}

		block, err := h.keeper.GetBlockByHash(ctx, blockHash)
		if errors.Is(err, ethereum.NotFound) {
			h.logger.Error("Preblock: Block not found", "hash", blockHash)
			err := h.keeper.CacheBlockHash(ctx, skipBlockHash)
			return err
		}
		if err != nil {
			h.logger.Error("PreBlocker error get block by hash error", "err", err)
			os.Exit(0) // panic recovers
		}

		block, err = h.keeper.GetBlockByNumber(ctx, block.Number())
		if err != nil {
			h.logger.Error("PreBlocker error get block by hash error", "err", err)
			os.Exit(0) // panic recovers
		}
		// very specific error caused by finalized check bug, ideally this check shouldn't exist
		if block.Hash().String() != blockHash {
			h.logger.Error("Preblock: Block is not finalized", "hash", blockHash)
			err := h.keeper.CacheBlockHash(ctx, skipBlockHash)
			return err
		}

		if block.Time() < h.prevBlockTime || int64(block.Time()) >= ctx.HeaderInfo().Time.Unix() || block.Time() < h.keeper.GetMinBlockTimestamp(ctx) {
			err := h.keeper.CacheBlockHash(ctx, skipBlockHash)
			return err
		}

		if err := h.keeper.CacheBlockHash(ctx, stakingtypes.CachedBlockHash{BlockHash: blockHash, Height: req.Height}); err != nil {
			return err
		}

		h.prevBlockTime = block.Time()

		return nil
	}
}
