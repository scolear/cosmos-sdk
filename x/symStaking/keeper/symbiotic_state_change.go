package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"strings"

	"cosmossdk.io/math"
	stakingtypes "cosmossdk.io/x/symStaking/types"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Struct to unmarshal the response from the Beacon Chain API
type FinalityUpdate struct {
	Data struct {
		FinalizedHeader struct {
			ExecutionPayloadHeader struct {
				BlockHash string `json:"block_hash"`
			} `json:"execution_payload_header"`
		} `json:"finalized_header"`
		AttestedHeader struct {
			ExecutionPayloadHeader struct {
				BlockHash string `json:"block_hash"`
			} `json:"execution_payload_header"`
		} `json:"attested_header"`
	} `json:"data"`
}

type RPCRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type RPCResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Validator struct {
	Stake    *big.Int
	ConsAddr [32]byte
}

const (
	FINALITY_UPDATE_PATH            = "/eth/v1/beacon/light_client/finality_update"
	GET_VALIDATOR_SET_FUNCTION_NAME = "getValidatorSet"
	GET_CURRENT_EPOCH_SELECTOR      = "b97dd9e2"
	GET_VALIDATOR_SET_ABI           = `[{
		"type": "function",
		"name": "getValidatorSet",
		"inputs": [
			{
				"name": "epoch",
				"type": "uint48",
				"internalType": "uint48"
			}
		],
		"outputs": [
			{
				"name": "validatorsData",
				"type": "tuple[]",
				"internalType": "struct SimpleMiddleware.ValidatorData[]",
				"components": [
					{
						"name": "stake",
						"type": "uint256",
						"internalType": "uint256"
					},
					{
						"name": "consAddr",
						"type": "bytes32",
						"internalType": "bytes32"
					}
				]
			}
		],
		"stateMutability": "view"
	}]`
)

func (k Keeper) SymbioticUpdateValidatorsPower(ctx context.Context) (string, error) {
	blockHash, err := k.getFinalizedBlockHash()
	if err != nil {
		return "", err
	}

	validators, err := k.GetSymbioticValidatorSet(ctx, blockHash)
	if err != nil {
		return "", err
	}

	for _, v := range validators {
		val, err := k.GetValidatorByConsAddr(ctx, v.ConsAddr[:])
		if err != nil {
			if errors.Is(err, stakingtypes.ErrNoValidatorFound) {
				continue
			}
			return "", err
		}

		k.SetValidatorTokens(ctx, val, math.NewIntFromBigInt(v.Stake))
	}

	return blockHash, nil
}

// Function to get the finality slot from the Beacon Chain API
func (k Keeper) getFinalizedBlockHash() (string, error) {
	resp, err := http.Get(k.beaconAPIURL + FINALITY_UPDATE_PATH)
	if err != nil {
		return "", fmt.Errorf("error making HTTP request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	var finalityUpdate FinalityUpdate
	err = json.Unmarshal(body, &finalityUpdate)
	if err != nil {
		return "", fmt.Errorf("error unmarshaling JSON: %v", err)
	}

	if k.debug {
		return finalityUpdate.Data.AttestedHeader.ExecutionPayloadHeader.BlockHash, nil
	}

	return finalityUpdate.Data.FinalizedHeader.ExecutionPayloadHeader.BlockHash, nil
}

// Function to get the finality slot from the Beacon Chain API
func (k Keeper) getCurrentEpoch(ctx context.Context, blockHash string) (*big.Int, error) {
	client, err := ethclient.Dial(k.ethAPIURL)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	contractAddress := common.HexToAddress(k.networkMiddlewareAddress)

	query := ethereum.CallMsg{
		To:   &contractAddress,
		Data: common.Hex2Bytes(GET_CURRENT_EPOCH_SELECTOR),
	}
	result, err := client.CallContractAtHash(ctx, query, common.HexToHash(blockHash))
	if err != nil {
		return nil, err
	}

	return new(big.Int).SetBytes(result), nil
}

func (k Keeper) GetSymbioticValidatorSet(ctx context.Context, blockHash string) ([]Validator, error) {
	client, err := ethclient.Dial(k.ethAPIURL)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	currentEpoch, err := k.getCurrentEpoch(ctx, blockHash)
	if err != nil {
		return nil, err
	}

	contractABI, err := abi.JSON(strings.NewReader(GET_VALIDATOR_SET_ABI))
	if err != nil {
		return nil, err
	}

	contractAddress := common.HexToAddress(k.networkMiddlewareAddress)
	data, err := contractABI.Pack(GET_VALIDATOR_SET_FUNCTION_NAME, currentEpoch)
	if err != nil {
		return nil, err
	}

	query := ethereum.CallMsg{
		To:   &contractAddress,
		Data: data,
	}
	result, err := client.CallContractAtHash(ctx, query, common.HexToHash(blockHash))
	if err != nil {
		return nil, err
	}

	var validators []Validator
	err = contractABI.UnpackIntoInterface(&validators, GET_VALIDATOR_SET_FUNCTION_NAME, result)
	if err != nil {
		return nil, err
	}

	return validators, nil
}
