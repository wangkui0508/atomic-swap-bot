package bot

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	gethcmn "github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/gcash/bchd/bchec"
	"github.com/gcash/bchd/btcjson"
	"github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchutil"
	log "github.com/sirupsen/logrus"

	"github.com/smartbch/atomic-swap-bot/htlcbch"
	"github.com/smartbch/atomic-swap-bot/htlcsbch"
)

/*
action & state:
 +--------+  +========+
 | action |  | state  |
 +--------+  +========+

BCH=>SBCH, normal flow:
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 +----------+    +----------+    +-------------+    +-----------+    +------------+    +-----------+
 |   user   |    |   bot    |    | bot(master) |    |   user    |    |    bot     |    |    bot    |
 +----------+ => +----------+ => +-------------+ => +-----------+ => +------------+ => +-----------+
 | send BCH |    | find BCH |    | send sBCH   |    | send sBCH |    | find sBCH  |    | send BCH  |
 | lock tx  |    | lock tx  |    |  lock tx    |    | unlock tx |    | unlock log |    | unlock tx |
 +----------+    +----------+    +-------------+    +-----------+    +------------+    +-----------+
                      /               /                     _______________/       __________/
                     /               /                     /                      /
                +=====+      +============+      +================+      +==============+
                | New | ---> | SbchLocked | ---> | SecretRevealed | ---> |  BchUnlocked |
                +=====+      +============+      +================+      +==============+
                                     \
                                      \
                                 +-------------+
                                 | bot(slave)  |
                                 +-------------+
                                 |  find sBCH  |
                                 |  lock log   |
                                 +-------------+
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

BCH=>SBCH, refund:
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 +----------+    +----------+    +-------------+      +-----------+
 |   user   |    |   bot    |    | bot(master) |      |    bot    |
 +----------+ => +----------+ => +-------------+ ===> +-----------+
 | send BCH |    | find BCH |    | send sBCH   |      | send sBCH |
 | lock tx  |    | lock tx  |    |  lock tx    |      | refund tx |
 +----------+    +----------+    +-------------+      +-----------+
                      /               /                     /
                     /               /                     /
                +=====+      +============+      +==============+
                | New | ---> | SbchLocked | ---> | SbchRefunded |
                +=====+      +============+      +==============+
                                     \
                                      \
                                 +-------------+
                                 | bot(slave)  |
                                 +-------------+
                                 |  find sBCH  |
                                 |  lock log   |
                                 +-------------+
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

SBCH=>BCH, normal flow:
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 +----------+    +----------+    +-------------+    +-----------+    +------------+    +-----------+
 |   user   |    |   bot    |    | bot(master) |    |   user    |    |    bot     |    |    bot    |
 +----------+ => +----------+ => +-------------+ => +-----------+ => +------------+ => +-----------+
 | send sBCH|    | find sBCH|    | send BCH    |    | send BCH  |    | find BCH   |    | send sBCH |
 | lock tx  |    | lock log |    |  lock tx    |    | unlock tx |    | unlock tx  |    | unlock tx |
 +----------+    +----------+    +-------------+    +-----------+    +------------+    +-----------+
                      /               /                     _______________/       __________/
                     /               /                     /                      /
                +=====+      +============+      +================+      +==============+
                | New | ---> |  BchLocked | ---> | SecretRevealed | ---> | SbchUnlocked |
                +=====+      +============+      +================+      +==============+
                                     \
                                      \
                                 +-------------+
                                 | bot(slave)  |
                                 +-------------+
                                 |  find BCH   |
                                 |  lock tx    |
                                 +-------------+
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

SBCH=>BCH, refund:
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 +----------+    +----------+    +-------------+      +-----------+
 |   user   |    |   bot    |    | bot(master) |      |    bot    |
 +----------+ => +----------+ => +-------------+ ===> +-----------+
 | send sBCH|    | find sBCH|    | send BCH    |      | send BCH  |
 | lock tx  |    | lock log |    |  lock tx    |      | refund tx |
 +----------+    +----------+    +-------------+      +-----------+
                      /               /                     /
                     /               /                     /
                +=====+      +============+      +==============+
                | New | ---> |  BchLocked | ---> |  BchRefunded |
                +=====+      +============+      +==============+
                                     \
                                      \
                                 +-------------+
                                 | bot(slave)  |
                                 +-------------+
                                 |  find BCH   |
                                 |  lock tx    |
                                 +-------------+
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
*/

/*
M: master, S: slave

+-------------------------+-+-+----------------+----------------+
| BCH2SBCH: normal        |M|S| old status     | new status     |
+-------------------------+-+-+----------------+----------------+
| handleBchDepositTxB2S   |✓|✓|                | New            |
| handleBchUserDeposits   |✓| | New            | SbchLocked     |
| handleSbchLockEventB2S  | |✓| New            | SbchLocked     |
| handleSbchUnlockEvent   |✓|✓| SbchLocked     | SecretRevealed |
| unlockBchUserDeposits   |✓|✓| SecretRevealed | BchUnlocked    |
+-------------------------+-+-+----------------+----------------+
+-------------------------+-+-+----------------+----------------+
| BCH2SBCH: refund        |M|S| old status     | new status     |
+-------------------------+-+-+----------------+----------------+
| handleBchDepositTxB2S   |✓|✓|                | New            |
| handleBchUserDeposits   |✓| | New            | SbchLocked     |
| handleSbchLockEventB2S  | |✓| New            | SbchLocked     |
| refundLockedSbch        |✓|✓| SbchLocked     | SbchRefunded   |
+-------------------------+-+-+----------------+----------------+
+-------------------------+-+-+----------------+----------------+
| BCH2SBCH: too late      |M|S| old status     | new status     |
+-------------------------+-+-+----------------+----------------+
| handleBchDepositTxB2S   |✓|✓|                | New            |
| handleBchUserDeposits   |✓| | New            | TooLate        |
+-------------------------+-+-+----------------+----------------+

+-------------------------+-+-+----------------+----------------+
| SBCH2BCH: normal        |M|S| old status     | new status     |
+-------------------------+-+-+----------------+----------------+
| handleSbchLockEventS2B  |✓|✓|                | New            |
| handleSbchUserDeposits  |✓| | New            | BchLocked      |
| handleBchDepositTxS2B   | |✓| New            | BchLocked      |
| handleBchReceiptTx      |✓|✓| BchLocked      | SecretRevealed |
| unlockSbchUserDeposits  |✓|✓| SecretRevealed | SbchUnlocked   |
+-------------------------+-+-+----------------+----------------+
+-------------------------+-+-+----------------+----------------+
| SBCH2BCH: refund        |M|S| old status     | new status     |
+-------------------------+-+-+----------------+----------------+
| handleSbchLockEventS2B  |✓|✓|                | New            |
| handleSbchUserDeposits  |✓| | New            | BchLocked      |
| handleBchDepositTxS2B   | |✓| New            | BchLocked      |
| refundLockedBCH         |✓|✓| BchLocked      | BchRefunded    |
+-------------------------+-+-+----------------+----------------+
+-------------------------+-+-+----------------+----------------+
| SBCH2BCH: too late      |M|S| old status     | new status     |
+-------------------------+-+-+----------------+----------------+
| handleSbchLockEventS2B  |✓|✓|                | New            |
| handleSbchUserDeposits  |✓| | New            | TooLate        |
+-------------------------+-+-+----------------+----------------+

*/

const (
	slaveDelayBchBlocks = 1
	slaveDelaySeconds   = 600 // 10m
)

type MarketMakerBot struct {
	db      DB
	bchCli  IBchClient
	sbchCli ISbchClient

	// BCH key
	bchPrivKey *bchec.PrivateKey
	bchPkh     []byte
	bchAddr    bchutil.Address // P2PKH

	// sBCH key
	sbchPrivKey *ecdsa.PrivateKey
	sbchAddr    gethcmn.Address

	// HTLC params
	bchTimeLock  uint16 // in blocks
	sbchTimeLock uint32 // in seconds
	penaltyRatio uint16 // in BPS

	// bot params
	serviceFeeRatio       uint16 // in BPS
	minSwapVal            uint64 // in sats
	maxSwapVal            uint64 // in sats
	bchConfirmations      uint8
	bchLockMinerFeeRate   uint64 // sats/byte
	bchUnlockMinerFeeRate uint64 // sats/byte
	bchRefundMinerFeeRate uint64 // sats/byte
	dbQueryLimit          int
	isSlaveMode           bool
	lazyMaster            bool // debug only
}

func NewBot(
	dbFile string,
	bchPrivKeyWIF, sbchPrivKeyHex string, // master mode
	bchMasterAddr, sbchMasterAddr string, // slave mode
	bchRpcUrl, sbchRpcUrl string,
	sbchHtlcAddr gethcmn.Address,
	sbchGasPrice *big.Int,
	bchConfirmations uint8,
	bchLockMinerFeeRate, bchUnlockMinerFeeRate, bchRefundMinerFeeRate uint64,
	sbchLockGasLimit, sbchUnlockGasLimit, sbchRefundGasLimit uint64,
	dbQueryLimit int,
	debugMode bool,
	slaveMode bool,
	lazyMaster bool, // debug only
) (*MarketMakerBot, error) {

	// load BCH key
	bchPrivKey, bchPbk, bchPkh, bchAddr, err := loadBchKey(
		bchPrivKeyWIF, bchMasterAddr, debugMode, slaveMode)
	if err != nil {
		return nil, fmt.Errorf("failed to load BCH private key: %w", err)
	}

	// load sBCH key
	sbchPrivKey, sbchAddr, err := loadSbchKey(sbchPrivKeyHex, sbchMasterAddr, slaveMode)
	if err != nil {
		return nil, fmt.Errorf("failed to load sBCH private key: %w", err)
	}

	// create RPC clients
	bchCli, err := NewBchClient(bchRpcUrl, bchAddr)
	if err != nil {
		return nil, fmt.Errorf("faield to create BCH RPC client: %w", err)
	}
	sbchCli, err := newSbchClient(sbchRpcUrl, 5*time.Second, sbchPrivKey, sbchAddr, sbchHtlcAddr,
		sbchGasPrice, sbchLockGasLimit, sbchUnlockGasLimit, sbchRefundGasLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to create sBCH RPC client: %w", err)
	}

	botInfo, err := sbchCli.getMarketMakerInfo(sbchAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to query bot info: %w", err)
	}

	if !bytes.Equal(bchPkh, botInfo.BchPkh[:]) {
		return nil, fmt.Errorf("BCH PKH mismatch: %s != %s",
			toHex(bchPkh), toHex(botInfo.BchPkh[:]))
	}

	// open DB
	db, err := OpenDB(dbFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open DB file: %w", err)
	}

	// print bot info
	log.Info("BCH pubkey  : ", "0x"+hex.EncodeToString(bchPbk))
	log.Info("BCH PKH     : ", "0x"+hex.EncodeToString(bchPkh))
	log.Info("BCH address : ", bchAddr.String())
	log.Info("sBCH address: ", sbchAddr.String())

	return &MarketMakerBot{
		db:                    db,
		bchCli:                bchCli,
		bchPrivKey:            bchPrivKey,
		bchPkh:                bchPkh,
		bchAddr:               bchAddr,
		sbchCli:               sbchCli,
		sbchPrivKey:           sbchPrivKey,
		sbchAddr:              sbchAddr,
		bchTimeLock:           botInfo.BchLockTime,
		sbchTimeLock:          botInfo.SbchLockTime,
		penaltyRatio:          botInfo.PenaltyBPS,
		serviceFeeRatio:       botInfo.FeeBPS,
		minSwapVal:            weiToSats(botInfo.MinSwapAmt),
		maxSwapVal:            weiToSats(botInfo.MaxSwapAmt),
		bchLockMinerFeeRate:   bchLockMinerFeeRate,
		bchUnlockMinerFeeRate: bchUnlockMinerFeeRate,
		bchRefundMinerFeeRate: bchRefundMinerFeeRate,
		bchConfirmations:      bchConfirmations,
		dbQueryLimit:          dbQueryLimit,
		isSlaveMode:           slaveMode,
		lazyMaster:            debugMode && lazyMaster,
	}, nil
}

func loadBchKey(privKeyWIF, masterAddr string, debugMode, slaveMode bool,
) (privKey *bchec.PrivateKey, pubKey, pkh []byte, addr *bchutil.AddressPubKeyHash, err error) {

	params := getBchParams(debugMode)
	if !slaveMode {
		// master mode

		var wif *bchutil.WIF
		wif, err = bchutil.DecodeWIF(privKeyWIF)
		if err != nil {
			err = fmt.Errorf("failed to decode WIF: %w", err)
			return
		}

		privKey = wif.PrivKey
		pubKey = privKey.PubKey().SerializeCompressed()
		pkh = bchutil.Hash160(pubKey)
		addr, err = bchutil.NewAddressPubKeyHash(pkh, params)
		if err != nil {
			err = fmt.Errorf("failed to derive BCH address: %w", err)
		}
		return
	}

	// slave mode

	if masterAddr == "" {
		err = fmt.Errorf("missing bchMasterAddr")
		return
	}

	_addr, _err := bchutil.DecodeAddress(masterAddr, params)
	if _err != nil {
		err = fmt.Errorf("failed to decode master address: %w", _err)
		return
	}

	ok := false
	addr, ok = _addr.(*bchutil.AddressPubKeyHash)
	if !ok {
		err = fmt.Errorf("failed to cast master address")
		return
	}

	pkh = addr.Hash160()[:]

	privKey = nil
	pubKey = nil
	return
}

func loadSbchKey(privKeyHex, masterAddr string, slaveMode bool,
) (privKey *ecdsa.PrivateKey, addr gethcmn.Address, err error) {

	privKey, err = gethcrypto.HexToECDSA(privKeyHex)
	if err != nil {
		err = fmt.Errorf("failed to load sBCH private key: %w", err)
		return
	}

	if !slaveMode {
		// master mode
		addr = gethcrypto.PubkeyToAddress(privKey.PublicKey)
		return
	}

	// slave mode

	if masterAddr == "" {
		err = fmt.Errorf("missing sbchMasterAddr")
		return
	}

	addr = gethcmn.HexToAddress(masterAddr)
	return
}

func getBchParams(debugMode bool) *chaincfg.Params {
	if debugMode {
		return &chaincfg.TestNet3Params
	}
	return &chaincfg.MainNetParams
}

func (bot *MarketMakerBot) PrepareDB() {
	_, err := bot.db.getLastHeights()
	if err == nil || !strings.HasPrefix(err.Error(), "no such table") {
		return
	}

	log.Info("init DB, sync schemas ...")
	if err = bot.db.syncSchemas(); err != nil {
		log.Fatal(err)
	}
	log.Info("init last BCH & sBCH heights ...")
	if err = bot.db.initLastHeights(0, 0); err != nil {
		log.Fatal(err)
	}
}

func (bot *MarketMakerBot) GetUTXOs() ([]btcjson.ListUnspentResult, error) {
	return bot.bchCli.getAllUTXOs()
}

func (bot *MarketMakerBot) Loop() {
	for {
		log.Info("---------- ", time.Now(), "' ----------")
		bot.refundLockedSbch()
		gotNewBlocks := bot.scanBchBlocks()
		bot.refundLockedBCH(gotNewBlocks)
		bot.handleBchUserDeposits()
		bot.unlockBchUserDeposits()
		bot.scanSbchEvents()
		bot.handleSbchUserDeposits()
		bot.unlockSbchUserDeposits()
		time.Sleep(2 * time.Second)
	}
}

// scan & handle BCH blocks
func (bot *MarketMakerBot) scanBchBlocks() (gotNewBlocks bool) {
	log.Info("scan BCH blocks ...")
	lastBlockNum, err := bot.db.getLastBchHeight()
	if err != nil {
		log.Fatal("DB error, failed to get last BCH height: ", err)
		return
	}
	log.Info("last BCH height: ", lastBlockNum)

	latestBlockNum, err := bot.bchCli.getBlockCount()
	if err != nil {
		log.Error("RPC error, failed to get BCH height: ", err)
		return
	}
	log.Info("latest BCH height: ", latestBlockNum)

	safeNewBlockNum := latestBlockNum - int64(bot.bchConfirmations)

	if lastBlockNum == 0 {
		lastBlockNum = uint64(safeNewBlockNum) - 1
		log.Info("init last BCH height: ", lastBlockNum)
	}

	for h := int64(lastBlockNum) + 1; h <= safeNewBlockNum; h++ {
		if !bot.handleBchBlock(h) {
			break
		}
	}

	gotNewBlocks = safeNewBlockNum > int64(lastBlockNum)
	return gotNewBlocks
}

// handle BCH lock|unlock|refund txs
func (bot *MarketMakerBot) handleBchBlock(h int64) bool {
	//log.Info("get BCH block#", h, " ...")
	block, err := bot.bchCli.getBlock(h)
	if err != nil {
		log.Error("RPC error, failed to get BCH block#", h, " : ", err)
		return false
	}
	log.Info("got BCH block#", h)

	bot.handleBchDepositTxs(uint64(h), block)
	bot.handleBchReceiptTxs(block)

	err = bot.db.setLastBchHeight(uint64(h))
	if err != nil {
		log.Fatal("DB error, failed to update last BCH height: ", err)
	}

	return true
}

// find and handle BCH lock txs
func (bot *MarketMakerBot) handleBchDepositTxs(h uint64, block *btcjson.GetBlockVerboseTxResult) {
	deposits := htlcbch.GetHtlcLocksInfo(block)
	log.Info("HTLC deposits: ", len(deposits))
	for _, deposit := range deposits {
		log.Info("HTLC deposit: ", toJSON(deposit))
		bot.handleBchDepositTxB2S(h, deposit)
		bot.handleBchDepositTxS2B(h, deposit)
	}
}

// create bch2sbch records (status=new)
func (bot *MarketMakerBot) handleBchDepositTxB2S(h uint64, deposit *htlcbch.HtlcLockInfo) {
	log.Info("handleBchDepositTxB2S")
	if !bytes.Equal(deposit.RecipientPkh, bot.bchPkh) {
		log.Info("not send to me, recipientPkh: ",
			toHex(deposit.RecipientPkh))
		return
	}
	if deposit.Expiration != bot.bchTimeLock {
		log.Infof("invalid expiration: %d != %d",
			deposit.Expiration, bot.bchTimeLock)
		return
	}
	if deposit.PenaltyBPS != bot.penaltyRatio {
		log.Infof("invalid penaltyRatio: %d != %d",
			deposit.PenaltyBPS, bot.penaltyRatio)
		return
	}
	if deposit.Value < bot.minSwapVal ||
		(bot.maxSwapVal > 0 && deposit.Value > bot.maxSwapVal) {

		log.Infof("value out of range: %d ∉ [%d, %d]",
			deposit.Value, bot.minSwapVal, bot.maxSwapVal)
		return
	}

	err := bot.db.addBch2SbchRecord(&Bch2SbchRecord{
		BchLockHeight:  h,
		BchLockTxHash:  deposit.TxHash,
		Value:          deposit.Value,
		RecipientPkh:   toHex(deposit.RecipientPkh),
		SenderPkh:      toHex(deposit.SenderPkh),
		HashLock:       toHex(deposit.HashLock),
		TimeLock:       uint32(deposit.Expiration),
		PenaltyBPS:     deposit.PenaltyBPS,
		SenderEvmAddr:  toHex(deposit.SenderEvmAddr),
		HtlcScriptHash: toHex(deposit.ScriptHash),
	})
	if err != nil {
		log.Error("DB error, failed to save BCH2SBCH record: ", err)
	}
}

// for sbch2bch record, change status from New to BchLocked
func (bot *MarketMakerBot) handleBchDepositTxS2B(h uint64, deposit *htlcbch.HtlcLockInfo) {
	if !bot.isSlaveMode {
		return
	}

	log.Info("handleBchDepositTxS2B")

	if !bytes.Equal(deposit.SenderPkh, bot.bchPkh) {
		log.Info("not locked by me, senderPkh: ",
			toHex(deposit.SenderPkh))
		return
	}

	hashLock := toHex(deposit.HashLock)
	record, err := bot.db.getSbch2BchRecordByHashLock(hashLock)
	if err != nil {
		log.Info("DB error, Sbch2BchRecord not found, hashLock: ", hashLock)
	}

	// TODO: add more checks

	record.UpdateStatusToBchLocked(deposit.TxHash)
	err = bot.db.updateSbch2BchRecord(record)
	if err != nil {
		log.Error("DB error, failed to update status of SBCH2BCH record: ", err)
	}
}

// find and handle BCH unlock txs
func (bot *MarketMakerBot) handleBchReceiptTxs(block *btcjson.GetBlockVerboseTxResult) {
	receipts := htlcbch.GetHtlcUnlocksInfo(block)
	log.Info("HTLC receipts: ", len(receipts))
	for _, receipt := range receipts {
		log.Info("HTLC receipt:", toJSON(receipt))
		bot.handleBchReceiptTx(receipt)
	}
}

// for sbch2bch records, change status from BchLocked to SecretRevealed
func (bot *MarketMakerBot) handleBchReceiptTx(receipt *htlcbch.HtlcUnlockInfo) {
	log.Info("handleBchReceiptTx")
	record, err := bot.db.getSbch2BchRecordByBchLockTxHash(receipt.PrevTxHash)
	if err != nil {
		log.Infof("can not get Sbch2BchRecord, BchLockTxHash=%s",
			receipt.TxHash)
		return
	}
	//log.Info(record)

	hashLock := secretToHashLock(gethcmn.FromHex(receipt.Secret))
	if hashLock != record.HashLock {
		log.Warnf("hashLock not match! secret: %s => hashLock: %s, DB hashLock: %s, ",
			receipt.Secret, hashLock, record.HashLock)
		return
	}

	//if record.Status != Sbch2BchStatusBchLocked {
	//	log.Infof("wrong status: %s", toJSON(record))
	//	continue
	//}

	record.UpdateStatusToSecretRevealed(receipt.Secret, receipt.TxHash)
	err = bot.db.updateSbch2BchRecord(record)
	if err != nil {
		log.Error("DB error, failed to update status of SBCH2BCH record: ", err)
	}
}

func (bot *MarketMakerBot) scanSbchEvents() {
	log.Info("scan sBCH events ...")
	lastBlockNum, err := bot.db.getLastSbchHeight()
	if err != nil {
		log.Fatal("failed to get last height of smartBCH from DB: ", err)
		return
	}
	log.Info("last sBCH height: ", lastBlockNum)

	newBlockNum, err := bot.sbchCli.getBlockNumber()
	if err != nil {
		log.Error("failed to get height of smartBCH: ", err)
		return
	}
	log.Info("latest sBCH height: ", newBlockNum)

	if lastBlockNum == 0 {
		lastBlockNum = newBlockNum - 1
		log.Info("init last sBCH height: ", lastBlockNum)
	}

	blockBatch := uint64(200)
	for fromH := lastBlockNum + 1; fromH <= newBlockNum; fromH += blockBatch {
		toH := fromH + blockBatch - 1
		if toH > newBlockNum {
			toH = newBlockNum
		}
		if !bot.handleSbchEvents(fromH, toH) {
			break
		}
	}
}

func (bot *MarketMakerBot) handleSbchEvents(fromH, toH uint64) bool {
	logs, err := bot.sbchCli.getHtlcLogs(fromH, toH)
	if err != nil {
		log.Error("failed to get smartBCH logs: ", err)
		return false
	}
	log.Infof("sBCH logs (block#%d ~ block#%d): %d",
		fromH, toH, len(logs))

	for _, ethLog := range logs {
		log.Info("sBCH log: ", toJSON(ethLog))
		switch ethLog.Topics[0] {
		case htlcsbch.LockEventId:
			bot.handleSbchLockEventS2B(ethLog)
			bot.handleSbchLockEventB2S(ethLog)
		case htlcsbch.UnlockEventId:
			bot.handleSbchUnlockEvent(ethLog)
		}
	}

	err = bot.db.setLastSbchHeight(toH)
	if err != nil {
		log.Fatal("DB error, failed to update last sBCH height: ", err)
	}

	return true
}

// find sBCH lock events, create sbch2bch records (status = new)
func (bot *MarketMakerBot) handleSbchLockEventS2B(ethLog gethtypes.Log) {
	lockLog := htlcsbch.ParseHtlcLockLog(ethLog)
	if lockLog == nil {
		return
	}

	if lockLog.UnlockerAddr != bot.sbchAddr {
		log.Info("not locked to me",
			", unlockerAddr: ", lockLog.UnlockerAddr.String(),
			//", botAddr: ", bot.sbchAddr.String(),
		)
		return
	}

	zeroAddr := gethcmn.Address{}
	if lockLog.BchRecipientPkh == zeroAddr {
		log.Info("BchRecipientPkh is zero, skip")
		return
	}

	penaltyBPS := lockLog.PenaltyBPS
	if penaltyBPS != bot.penaltyRatio {
		log.Infof("invalid penaltyRatio: %d != %d",
			penaltyBPS, bot.penaltyRatio)
		return
	}

	sbchTimeLock := uint32(lockLog.UnlockTime - lockLog.CreatedTime)
	if sbchTimeLock != bot.sbchTimeLock {
		log.Infof("invalid TimeLock: %d != %d",
			sbchTimeLock, bot.sbchTimeLock)
		return
	}

	valSats := weiToSats(lockLog.Value)
	if valSats < bot.minSwapVal ||
		(bot.maxSwapVal > 0 && valSats > bot.maxSwapVal) {

		log.Infof("value out of range: %d ∉ [%d, %d]",
			valSats, bot.minSwapVal, bot.maxSwapVal)
		return
	}

	log.Info("got a sBCH Lock log: ", toJSON(lockLog))
	bchTimeLock := sbchTimeLockToBlocks(sbchTimeLock) / 2
	covenant, err := htlcbch.NewMainnetCovenant(bot.bchPkh,
		lockLog.BchRecipientPkh[:], lockLog.HashLock[:], bchTimeLock, 0)
	if err != nil {
		log.Error("failed to create HTLC covenant: ", err)
		return
	}

	scriptHash, err := covenant.GetRedeemScriptHash()
	if err != nil {
		log.Error("failed to get script hash: ", err)
		return
	}

	err = bot.db.addSbch2BchRecord(&Sbch2BchRecord{
		SbchLockTime:    lockLog.CreatedTime,
		SbchLockTxHash:  toHex(ethLog.TxHash[:]),
		Value:           valSats,
		SbchSenderAddr:  toHex(lockLog.LockerAddr[:]),
		BchRecipientPkh: toHex(lockLog.BchRecipientPkh[:]),
		HashLock:        toHex(lockLog.HashLock[:]),
		TimeLock:        sbchTimeLock,
		PenaltyBPS:      penaltyBPS,
		HtlcScriptHash:  toHex(scriptHash),
	})
	if err != nil {
		log.Error("DB error, failed to save SBCH2BCH record: ", err)
	}
}

// bch2sbch record: New => SbchLocked
func (bot *MarketMakerBot) handleSbchLockEventB2S(ethLog gethtypes.Log) {
	if !bot.isSlaveMode {
		return
	}

	lockLog := htlcsbch.ParseHtlcLockLog(ethLog)
	if lockLog == nil {
		return
	}

	if lockLog.LockerAddr != bot.sbchAddr {
		log.Info("not opened by master",
			", lockerAddr: ", lockLog.LockerAddr.String(),
			//", botAddr: ", bot.sbchAddr.String(),
		)
		return
	}

	log.Info("got a sBCH Lock log (slave): ", toJSON(lockLog))

	record, err := bot.db.getBch2SbchRecordByHashLock(toHex(lockLog.HashLock[:]))
	if err != nil {
		log.Error("DB error:", err)
		return
	}

	if record.Status != Bch2SbchStatusNew {
		return
	}

	txTime, err := bot.sbchCli.getTxTime(ethLog.TxHash)
	if err != nil {
		log.Error("RPC error, failed to get sBCH tx time:", err)
		txTime = uint64(time.Now().Unix())
	}

	record.UpdateStatusToSbchLocked(toHex(ethLog.TxHash[:]), txTime)
	err = bot.db.updateBch2SbchRecord(record)
	if err != nil {
		log.Error("DB error, failed to update status of BCH2SBCH record: ", err)
	}
}

// bch2sbch records: SbchLocked => SecretRevealed
func (bot *MarketMakerBot) handleSbchUnlockEvent(ethLog gethtypes.Log) {
	unlockLog := htlcsbch.ParseHtlcUnlockLog(ethLog)
	if unlockLog == nil {
		return
	}

	log.Info("got a sBCH Unlock log: ", toJSON(unlockLog))
	hashLock := toHex(unlockLog.HashLock[:])
	record, err := bot.db.getBch2SbchRecordByHashLock(hashLock)
	//log.Info(record)
	if err != nil {
		log.Infof("can not get Bch2SbchRecord, hashLock=%s", hashLock)
		return
	}

	hashLock2 := secretToHashLock(unlockLog.Secret[:])
	if hashLock2 != hashLock {
		log.Warnf("hashLock not match! secret: %s => hashLock: %s, DB hashLock: %s, ",
			toHex(unlockLog.Secret[:]), hashLock2, hashLock)
		return
	}

	if record.Status != Bch2SbchStatusSbchLocked {
		return
	}

	record.UpdateStatusToSecretRevealed(toHex(unlockLog.Secret[:]), toHex(unlockLog.TxHash[:]))
	err = bot.db.updateBch2SbchRecord(record)
	if err != nil {
		log.Error("DB error, failed to update status of BCH2SBCH record: ", err)
		return
	}
}

// bch2sbch records: New => SbchLocked|TooLateToLockSbch
func (bot *MarketMakerBot) handleBchUserDeposits() {
	if bot.isSlaveMode {
		return
	}

	log.Info("handle BCH user deposits ...")
	records, err := bot.db.getBch2SbchRecordsByStatus(Bch2SbchStatusNew, bot.dbQueryLimit)
	if err != nil {
		log.Error("DB error, failed to get BCH2SBCH records: ", err)
		return
	}
	log.Info("unhandled BCH user deposits: ", len(records))

	for _, record := range records {
		log.Info("handle BCH user deposit: ", toJSON(record))

		//confirmations := currBlockNum - int64(record.BchLockHeight) + 1
		confirmations, err := bot.bchCli.getTxConfirmations(record.BchLockTxHash)
		if err != nil {
			log.Error("RPC error, failed to get tx confirmations: ", err)
			continue
		}

		// do not send sBCH to user if it's too late!
		if confirmations > int64(bot.bchTimeLock)/3 {
			log.Info("too late to lock sBCH",
				", confirmations: ", confirmations,
				", timeLock: ", record.TimeLock)
			record.Status = Bch2SbchStatusTooLateToLockSbch
			err = bot.db.updateBch2SbchRecord(record)
			if err != nil {
				log.Error("DB error, failed to update status of BCH2SBCH record: ", err)
			}

			continue
		}

		sbchTimeLock := bchTimeLockToSeconds(record.TimeLock) / 2
		bchValMinusFee := record.Value * (10000 - uint64(bot.serviceFeeRatio)) / 10000
		log.Info("sbchTimeLock: ", sbchTimeLock, " , bchValMinusFee: ", bchValMinusFee)

		txHash, err := bot.sbchCli.lockSbchToHtlc(
			gethcmn.HexToAddress(record.SenderEvmAddr),
			gethcmn.HexToHash(record.HashLock),
			sbchTimeLock,
			satsToWei(bchValMinusFee),
		)
		if err != nil {
			log.Error("RPC error, failed to lock sBCH to HTLC: ", err)
			continue
		}

		log.Info("lock sBCH successful",
			", hashLock: ", record.HashLock,
			", txHash: ", txHash.String())

		txTime, err := bot.sbchCli.getTxTime(*txHash)
		if err != nil {
			log.Error("RPC error, failed to get sBCH tx time:", err)
			txTime = uint64(time.Now().Unix())
		}

		record.UpdateStatusToSbchLocked(toHex(txHash[:]), txTime)
		err = bot.db.updateBch2SbchRecord(record)
		if err != nil {
			log.Error("DB error, failed to update status of BCH2SBCH record: ", err)
		}
	}
}

// sbch2bch records: New => BchLocked|TooLateToLockSbch
func (bot *MarketMakerBot) handleSbchUserDeposits() {
	if bot.isSlaveMode {
		return
	}

	log.Info("handle sBCH user deposits ...")

	lastBlockNum, err := bot.db.getLastBchHeight()
	if err != nil {
		log.Fatal("DB error, failed to get last BCH height: ", err)
		return
	}
	log.Info("last BCH height: ", lastBlockNum)

	records, err := bot.db.getSbch2BchRecordsByStatus(Sbch2BchStatusNew, bot.dbQueryLimit)
	if err != nil {
		log.Error("DB error, failed to get unhandled sBCH user deposits: ", err)
		return
	}
	log.Info("unhandled sBCH user deposits: ", len(records))

	for _, record := range records {
		log.Info("SBCH2BCH record: ", toJSON(record))

		bchValMinusFee := int64(record.Value) * (10000 - int64(bot.serviceFeeRatio)) / 10000
		utxos, err := bot.bchCli.getUTXOs(bchValMinusFee+5000, 10)
		if err != nil {
			log.Error("failed to get UTXOs: ", err)
			continue
		}
		log.Info("bchValMinusFee: ", bchValMinusFee, ", UTXOs:", toJSON(utxos))

		inputs := make([]htlcbch.InputInfo, len(utxos))
		for i, utxo := range utxos {
			inputs[i] = htlcbch.InputInfo{
				TxID:   gethcmn.FromHex(utxo.TxID),
				Vout:   utxo.Vout,
				Amount: utxoAmtToSats(utxo.Amount),
			}
		}

		currTime, err := bot.sbchCli.getBlockTimeLatest()
		if err != nil {
			log.Error("RPC error, failed to get sBCH time: ", err)
			continue
		}

		// do not send BCH to user if its too late!
		timeElapsed := currTime - record.SbchLockTime
		if uint32(timeElapsed) > bot.sbchTimeLock/3 {
			log.Info("too late to lock BCH, time elapsed: ", timeElapsed, ", timeLock: ", record.TimeLock)
			record.Status = Sbch2BchStatusTooLateToLockBch
			err = bot.db.updateSbch2BchRecord(record)
			if err != nil {
				log.Error("DB error, failed to update status of SBCH2BCH record: ", err)
			}

			continue
		} else {
			log.Info("time elapsed: ", timeElapsed, ", timeLock: ", record.TimeLock)
		}

		bchTimeLock := sbchTimeLockToBlocks(record.TimeLock) / 2
		log.Info("BCH timeLock: ", bchTimeLock)

		covenant, err := htlcbch.NewMainnetCovenant(
			bot.bchPkh,
			gethcmn.FromHex(record.BchRecipientPkh),
			gethcmn.FromHex(record.HashLock),
			bchTimeLock,
			0,
		)
		if err != nil {
			log.Error("failed to create HTLC covenant: ", err)
			continue
		}

		tx, err := covenant.MakeLockTx(
			bot.bchPrivKey,
			inputs,
			bchValMinusFee,
			bot.bchLockMinerFeeRate,
		)
		if err != nil {
			log.Error("failed to create BCH tx: ", err)
			continue
		}
		log.Info("BCH tx hex: ", htlcbch.MsgTxToHex(tx))

		txHash, err := bot.bchCli.SendTx(tx)
		if err != nil {
			log.Error("failed to send BCH tx: ", err)

			// more debug info
			//prevPkScript, _ := htlcbch.PayToPubKeyHashPkScript(bot.bchPkh)
			//log.Infof("meep debug --tx=%s --idx=%d --amt=%d --pkscript=%s",
			//	htlcbch.MsgTxToHex(tx), 0, utxoAmtToSats(utxo.Amount), toHex(prevPkScript))
			continue
		}
		log.Info("BCH tx sent, hash: ", txHash.String())

		record.UpdateStatusToBchLocked(txHash.String())
		err = bot.db.updateSbch2BchRecord(record)
		if err != nil {
			log.Error("DB error, failed to update status of SBCH2BCH record: ", err)
		}
	}
}

// bch2sbch records: SecretRevealed => BchUnlocked
func (bot *MarketMakerBot) unlockBchUserDeposits() {
	log.Info("unlock BCH user deposits ...")
	records, err := bot.db.getBch2SbchRecordsByStatus(Bch2SbchStatusSecretRevealed, bot.dbQueryLimit)
	if err != nil {
		log.Error("failed to get BCH2SBCH records from DB: ", err)
		return
	}
	log.Info("secret-revealed BCH user deposits: ", len(records))

	now := time.Now()
	for _, record := range records {
		log.Info("record: ", toJSON(record))
		if bot.isSlaveMode {
			if now.Sub(record.UpdatedAt).Seconds() < slaveDelaySeconds {
				// give master some time to handle it
				log.Info("wait master")
				continue
			}
		} else if bot.lazyMaster {
			if now.Sub(record.UpdatedAt).Seconds() < slaveDelaySeconds*2 {
				// give slave some time to handle it
				log.Info("wait slave")
				continue
			}
		}

		covenant, err := htlcbch.NewMainnetCovenant(
			gethcmn.FromHex(record.SenderPkh),
			gethcmn.FromHex(record.RecipientPkh),
			gethcmn.FromHex(record.HashLock),
			uint16(record.TimeLock),
			record.PenaltyBPS,
		)
		if err != nil {
			log.Error("failed to create HTLC covenant: ", err)
			continue
		}
		p2shAddr, _ := covenant.GetP2SHAddress()
		log.Info("covenant: ", p2shAddr)

		tx, err := covenant.MakeUnlockTx(
			gethcmn.FromHex(record.BchLockTxHash),
			0,
			int64(record.Value),
			bot.bchUnlockMinerFeeRate,
			gethcmn.FromHex(record.Secret),
		)
		if err != nil {
			log.Error("failed to create unlock tx: ", err)
			continue
		}
		log.Info("tx: ", htlcbch.MsgTxToHex(tx))

		txHashStr := "?"
		if txHash, err := bot.bchCli.SendTx(tx); err == nil {
			log.Info("BCH unlock tx sent, hash: ", txHash.String())
			txHashStr = txHash.String()
		} else {
			log.Error("failed to unlock BCH: ", err)
			if isUtxoSpentErr(err) {
				log.Info("UTXO is spent by others")
			} else {
				continue
			}
		}

		record.UpdateStatusToBchUnlocked(txHashStr)
		err = bot.db.updateBch2SbchRecord(record)
		if err != nil {
			log.Error("DB error, failed to update status of BCH2SBCH record: ", err)
		}
	}
}

// sbch2bch: SecretRevealed => SbchUnlocked
func (bot *MarketMakerBot) unlockSbchUserDeposits() {
	log.Info("unlock sBCH user deposits ...")
	records, err := bot.db.getSbch2BchRecordsByStatus(Sbch2BchStatusSecretRevealed, bot.dbQueryLimit)
	if err != nil {
		log.Error("DB error, failed to get SBCH2BCH records from DB: ", err)
		return
	}
	log.Info("secret-revealed sBCH user deposits: ", len(records))

	now := time.Now()
	for _, record := range records {
		log.Info("SBCH2BCH record: ", toJSON(record))
		if bot.isSlaveMode {
			if now.Sub(record.UpdatedAt).Seconds() < slaveDelaySeconds {
				// give master some time to handle it
				log.Info("wait master")
				continue
			}
		} else if bot.lazyMaster {
			if now.Sub(record.UpdatedAt).Seconds() < slaveDelaySeconds*2 {
				// give slave some time to handle it
				log.Info("wait slave")
				continue
			}
		}

		hashLock := gethcmn.HexToHash(record.HashLock)
		secret := gethcmn.HexToHash(record.Secret)

		txHashStr := "?"
		if txHash, err := bot.sbchCli.unlockSbchFromHtlc(hashLock, secret); err == nil {
			txHashStr = toHex(txHash[:])
			log.Info("sBCH unlock tx sent, hash: ", txHashStr)
		} else {
			log.Error("RPC error, failed to unlock sBCH: ", err)

			state, _ := bot.sbchCli.getSwapState(hashLock)
			if state == SwapUnlocked {
				log.Info("swap is unlockd")
			} else {
				continue
			}
		}

		record.UpdateStatusToSbchUnlocked(txHashStr)
		err = bot.db.updateSbch2BchRecord(record)
		if err != nil {
			log.Error("DB error, failed to update status of SBCH2BCH record: ", err)
		}
	}
}

// sbch2bch records: BchLocked => BchRefunded
func (bot *MarketMakerBot) refundLockedBCH(gotNewBlocks bool) {
	if !gotNewBlocks {
		return
	}

	log.Info("handle BCH refunds ...")

	records, err := bot.db.getSbch2BchRecordsByStatus(Sbch2BchStatusBchLocked, bot.dbQueryLimit)
	if err != nil {
		log.Error("DB error, failed to get SBCH2BCH records: ", err)
		return
	}
	log.Info("BchLocked SBCH2BCH records: ", len(records))

	for _, record := range records {
		log.Info("record: ", record.ID, ", txHash: ", record.BchLockTxHash)
		bchTimeLock := sbchTimeLockToBlocks(record.TimeLock) / 2
		//log.Info("BCH timeLock: ", bchTimeLock)

		requiredConfirmations := bchTimeLock
		if bot.isSlaveMode {
			// give master some time to handle it
			requiredConfirmations += slaveDelayBchBlocks
		} else if bot.lazyMaster {
			// give slave some time to handle it
			requiredConfirmations += slaveDelayBchBlocks * 2
		}

		confirmations, err := bot.bchCli.getTxConfirmations(record.BchLockTxHash)
		if err != nil {
			log.Error("RPC error, failed to get tx confirmations: ", err)
			continue
		}

		log.Info("confirmations: ", confirmations, " , bchTimeLock: ", bchTimeLock)
		if confirmations <= int64(requiredConfirmations) {
			continue
		}

		covenant, err := htlcbch.NewMainnetCovenant(
			bot.bchPkh,
			gethcmn.FromHex(record.BchRecipientPkh),
			gethcmn.FromHex(record.HashLock),
			bchTimeLock,
			0,
		)
		if err != nil {
			log.Error("failed to create HTLC covenant: ", err)
			log.Error("record:", toJSON(record))
			continue
		}

		bchValMinusFee := int64(record.Value) * (10000 - int64(bot.serviceFeeRatio)) / 10000
		tx, err := covenant.MakeRefundTx(
			gethcmn.FromHex(record.BchLockTxHash),
			0,
			bchValMinusFee,
			bot.bchRefundMinerFeeRate,
		)
		if err != nil {
			log.Error("failed to make refund tx: ", err)
			continue
		}
		log.Info("refund tx: ", htlcbch.MsgTxToHex(tx))

		txHashStr := "?"
		if txHash, err := bot.bchCli.SendTx(tx); err == nil {
			log.Info("BCH refund tx sent, hash: ", txHash.String())
			txHashStr = txHash.String()
		} else {
			log.Error("failed to refund BCH: ", err)
			if isUtxoSpentErr(err) {
				log.Info("UTXO is spent by others")
			} else {
				continue
			}
		}

		record.UpdateStatusToBchRefunded(txHashStr)
		err = bot.db.updateSbch2BchRecord(record)
		if err != nil {
			log.Error("DB error, failed to save SBCH2BCH record: ", err)
		}
	}
}

// bch2sbch records: SbchLocked => SbchRefunded
func (bot *MarketMakerBot) refundLockedSbch() {
	log.Info("handle sBCH refunds ...")

	records, err := bot.db.getBch2SbchRecordsByStatus(Bch2SbchStatusSbchLocked, bot.dbQueryLimit)
	if err != nil {
		log.Error("DB error, failed to get BCH2SBCH records: ", err)
		return
	}

	log.Info("SbchLocked BCH2SBCH records: ", len(records))
	if len(records) == 0 {
		return
	}

	sbchNow, err := bot.sbchCli.getBlockTimeLatest()
	if err != nil {
		log.Error("RPC error, failed to get sBCH time: ", err)
		return
	}
	log.Info("sbchNow: ", sbchNow)

	for _, record := range records {
		log.Info("record: ", record.ID,
			" , SbchLockTxHash: ", record.SbchLockTxHash,
			" , SbchLockTxTime: ", record.SbchLockTxTime)
		txTime := record.SbchLockTxTime
		sbchTimeLock := bchTimeLockToSeconds(record.TimeLock) / 2
		unlockableTime := txTime + uint64(sbchTimeLock)
		if bot.isSlaveMode {
			// give master some time to handle it
			unlockableTime += slaveDelaySeconds
		} else if bot.lazyMaster {
			// give slave some time to handle it
			unlockableTime += slaveDelaySeconds * 2
		}

		if sbchNow <= unlockableTime {
			log.Info("txTime: ", txTime, " unlockableTime: ", unlockableTime)
			continue
		}

		hashLock := gethcmn.HexToHash(record.HashLock)

		txHashStr := "?"
		if txHash, err := bot.sbchCli.refundSbchFromHtlc(hashLock); err == nil {
			txHashStr = toHex(txHash.Bytes())
			log.Info("sBCH refund tx sent, hash: ", txHashStr)
		} else {
			log.Error("RPC error, failed to refund sBCH: ", err)

			state, _ := bot.sbchCli.getSwapState(hashLock)
			if state == SwapRefunded {
				log.Info("swap is refunded")
			} else {
				continue
			}
		}

		record.UpdateStatusToSbchRefunded(txHashStr)
		err = bot.db.updateBch2SbchRecord(record)
		if err != nil {
			log.Error("DB error, failed to update status of BCH2SBCH record: ", err)
		}
	}
}

func secretToHashLock(secret []byte) string {
	hashLock := sha256.Sum256(secret)
	return toHex(hashLock[:])
}

func bchTimeLockToSeconds(nBlocks uint32) uint32 {
	return nBlocks * 10 * 60
}
func sbchTimeLockToBlocks(nSeconds uint32) uint16 {
	return uint16(nSeconds / (10 * 60))
}

func satsToWei(amt uint64) *big.Int {
	return big.NewInt(0).Mul(big.NewInt(int64(amt)), big.NewInt(1e10))
}
func weiToSats(amt *big.Int) uint64 {
	return big.NewInt(0).Div(amt, big.NewInt(1e10)).Uint64()
}

func utxoAmtToSats(amt float64) int64 {
	return int64(math.Round(amt * 1e8))
}

func toHex(bs []byte) string {
	return hex.EncodeToString(bs)
}

func toJSON(v interface{}) string {
	bs, _ := json.Marshal(v)
	return string(bs)
}
