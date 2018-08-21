// Copyright (C) 2018  MediBloc
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>

package core

import (
	"io/ioutil"

	"github.com/gogo/protobuf/proto"
	"github.com/medibloc/go-medibloc/common"
	"github.com/medibloc/go-medibloc/core/pb"
	"github.com/medibloc/go-medibloc/crypto/signature/algorithm"
	"github.com/medibloc/go-medibloc/storage"
	"github.com/medibloc/go-medibloc/util"
	"github.com/medibloc/go-medibloc/util/byteutils"
	"github.com/medibloc/go-medibloc/util/logging"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/sha3"
)

var (
	// GenesisHash is hash of genesis block
	GenesisHash = genesisHash("genesisHash")
	// GenesisTimestamp is timestamp of genesis block
	GenesisTimestamp = int64(0)
	// GenesisCoinbase coinbase address of genesis block
	GenesisCoinbase = common.HexToAddress("02fc22ea22d02fc2469f5ec8fab44bc3de42dda2bf9ebc0c0055a9eb7df579056c")
	// GenesisHeight is height of genesis block
	GenesisHeight = uint64(1)
)

func genesisHash(quote string) []byte {
	hasher := sha3.New256()
	hasher.Write([]byte(quote))
	return hasher.Sum(nil)
}

// LoadGenesisConf loads genesis conf file
func LoadGenesisConf(filePath string) (*corepb.Genesis, error) {
	buf, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	content := string(buf)

	genesis := new(corepb.Genesis)
	if err := proto.UnmarshalText(content, genesis); err != nil {
		return nil, err
	}
	return genesis, nil
}

// NewGenesisBlock generates genesis block
func NewGenesisBlock(conf *corepb.Genesis, consensus Consensus, sto storage.Storage) (*Block, error) {
	if conf == nil {
		return nil, ErrNilArgument
	}
	blockState, err := NewBlockState(consensus, sto)
	if err != nil {
		return nil, err
	}
	genesisBlock := &Block{
		BlockData: &BlockData{
			BlockHeader: &BlockHeader{
				hash:       GenesisHash,
				parentHash: GenesisHash,
				chainID:    conf.Meta.ChainId,
				coinbase:   GenesisCoinbase,
				reward:     util.NewUint128FromUint(0),
				timestamp:  GenesisTimestamp,
				alg:        algorithm.SECP256K1,
			},
			transactions: make([]*Transaction, 0),
			height:       GenesisHeight,
		},
		storage:   sto,
		state:     blockState,
		consensus: consensus,
		sealed:    false,
	}
	if err := genesisBlock.BeginBatch(); err != nil {
		return nil, err
	}
	dynasty := make([]common.Address, 0)
	for _, v := range conf.GetConsensus().GetDpos().GetDynasty() {
		member := common.HexToAddress(v)
		genesisBlock.State().dposState.PutCandidate(member)
		dynasty = append(dynasty, member)
	}
	genesisBlock.State().dposState.SetDynasty(dynasty)

	initialMessage := "Genesis block of MediBloc"
	payload := &DefaultPayload{
		Message: initialMessage,
	}
	payloadBuf, err := payload.ToBytes()
	if err != nil {
		return nil, err
	}

	initialTx := &Transaction{
		txType:    TxTyGenesis,
		from:      GenesisCoinbase,
		to:        GenesisCoinbase,
		value:     util.Uint128Zero(),
		timestamp: GenesisTimestamp,
		nonce:     1,
		chainID:   conf.Meta.ChainId,
		payload:   payloadBuf,
	}

	hash, err := initialTx.CalcHash()
	if err != nil {
		return nil, err
	}
	initialTx.hash = hash

	// Insert initial transaction
	err = genesisBlock.AcceptTransaction(initialTx)
	if err != nil {
		return nil, err
	}

	// Token distribution
	supply := util.NewUint128()
	for i, dist := range conf.TokenDistribution {
		addr := common.HexToAddress(dist.Address)
		balance, err := util.NewUint128FromString(dist.Value)
		if err != nil {
			if err := genesisBlock.RollBack(); err != nil {
				return nil, err
			}
			return nil, err
		}

		err = genesisBlock.state.AddBalance(addr, balance)
		if err != nil {
			logging.Console().WithFields(logrus.Fields{
				"err": err,
			}).Info("add balance failed at newGenesis")
			if err := genesisBlock.RollBack(); err != nil {
				return nil, err
			}




			return nil, err
		}

		tx := &Transaction{
			txType:    TxTyGenesis,
			from:      GenesisCoinbase,
			to:        addr,
			value:     balance,
			timestamp: GenesisTimestamp,
			nonce:     2 + uint64(i),
			chainID:   conf.Meta.ChainId,
		}
		hash, err = tx.CalcHash()
		if err != nil {
			return nil, err
		}
		tx.hash = hash

		err = genesisBlock.AcceptTransaction(tx)
		if err != nil {
			return nil, err
		}

		supply, err = supply.Add(balance)
		if err != nil {
			if err := genesisBlock.RollBack(); err != nil {
				return nil, err
			}
			return nil, err
		}
	}
	genesisBlock.supply = supply
	genesisBlock.state.supply = supply.DeepCopy()

	if err := genesisBlock.Commit(); err != nil {
		return nil, err
	}

	dataRoot, err := genesisBlock.state.dataState.RootBytes()
	if err != nil {
		return nil, err
	}

	dposRoot, err := genesisBlock.state.DposState().RootBytes()
	if err != nil {
		return nil, err
	}

	genesisBlock.accStateRoot = genesisBlock.state.AccountsRoot()
	genesisBlock.dataStateRoot = dataRoot
	genesisBlock.dposRoot = dposRoot

	genesisBlock.sealed = true

	return genesisBlock, nil
}

// CheckGenesisBlock checks if a block is genesis block
func CheckGenesisBlock(block *Block) bool {
	if block == nil {
		return false
	}
	if byteutils.Equal(block.Hash(), GenesisHash) {
		return true
	}
	return false
}

// CheckGenesisConf checks if block and genesis configuration match
func CheckGenesisConf(block *Block, genesis *corepb.Genesis) bool {
	if block.ChainID() != genesis.GetMeta().ChainId {
		logging.Console().WithFields(logrus.Fields{
			"block":   block,
			"genesis": genesis,
		}).Error("Genesis ChainID does not match.")
		return false
	}

	accounts, err := block.State().accState.Accounts()
	if err != nil {
		logging.Console().WithFields(logrus.Fields{
			"err": err,
		}).Error("Failed to get accounts from genesis block.")
		return false
	}

	dynasty, err := block.state.dposState.Dynasty()
	if err != nil {
		logging.Console().WithFields(logrus.Fields{
			"err": err,
		}).Error("Failed to get dynasty from genesis block")
		return false
	}

	for i, v := range dynasty {
		if genesis.Consensus.Dpos.Dynasty[i] != v.Hex() {
			return false
		}
	}
	dynastyCount := len(dynasty)
	if uint32(dynastyCount) != genesis.Meta.DynastySize {
		return false
	}

	tokenDist := genesis.GetTokenDistribution()
	if len(accounts)-1 != len(tokenDist) {
		logging.Console().WithFields(logrus.Fields{
			"accountCount": len(accounts),
			"tokenCount":   len(tokenDist),
		}).Error("Size of token distribution accounts does not match.")
		return false
	}

	for _, account := range accounts {
		if account.Address == GenesisCoinbase {
			continue
		}
		contains := false
		for _, token := range tokenDist {
			if token.Address == account.Address.Hex() {
				balance, err := util.NewUint128FromString(token.Value)
				if err != nil {
					logging.Console().WithFields(logrus.Fields{
						"err": err,
					}).Error("Failed to convert balance from string to uint128.")
					return false
				}
				if balance.Cmp(account.Balance) != 0 {
					logging.Console().WithFields(logrus.Fields{
						"balanceInBlock": account.Balance,
						"balanceInConf":  balance,
						"account":        account.Address.Hex(),
					}).Error("Genesis's token balance does not match.")
					return false
				}
				contains = true
				break
			}
		}
		if !contains {
			logging.Console().WithFields(logrus.Fields{
				"account": account.Address.Hex(),
			}).Error("Accounts of token distribution don't match.")
			return false
		}
	}

	return true
}
