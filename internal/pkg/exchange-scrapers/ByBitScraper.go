package scrapers

import (
	"encoding/json"
	"errors"
	"fmt"

	"strconv"

	"strings"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"

	"github.com/diadata-org/diadata/pkg/dia"
	"github.com/diadata-org/diadata/pkg/utils"
)

var RealTimeSocketURL string = "wss://stream.bybit.com/realtime"
var RealTimePublicSocketURL string = "wss://stream.bybit.com/realtime_public"
var ByBitSymbolsURL string = "https://api.bybit.com/v2/public/symbols"

// ByBitScraper provides  methods needed to get Trade information from ByBit
type ByBitScraper struct {
	realtime       *ws.Conn
	realtimepublic *ws.Conn

	// signaling channels for session initialization and finishing
	shutdown     chan nothing
	shutdownDone chan nothing
	// error handling; to read error or closed, first acquire read lock
	// only cleanup method should hold write lock
	errorLock sync.RWMutex
	error     error
	closed    bool
	// used to keep track of trading pairs that we subscribed to
	pairScrapers map[string]*ByBitPairScraper
	// exchange name
	exchangeName string
	// channel to send trades
	chanTrades chan *dia.Trade
}

type ByBitTradeResponse struct {
	Topic string `json:"topic"`
	Data  []struct {
		TradeTimeMs   int64     `json:"trade_time_ms"`
		Timestamp     time.Time `json:"timestamp"`
		Symbol        string    `json:"symbol"`
		Side          string    `json:"side"`
		Size          float64   `json:"size"`
		Price         float64   `json:"price"`
		TickDirection string    `json:"tick_direction"`
		TradeID       string    `json:"trade_id"`
		CrossSeq      int64     `json:"cross_seq"`
	} `json:"data"`
}

// Trade Response from the public socket
type ByBitPublicTradeResponse struct {
	Topic string `json:"topic"`
	Data  []struct {
		Symbol        string    `json:"symbol"`
		TickDirection string    `json:"tick_direction"`
		Price         string    `json:"price"`
		Size          float64   `json:"size"`
		Timestamp     time.Time `json:"timestamp"`
		TradeTimeMs   string    `json:"trade_time_ms"`
		Side          string    `json:"side"`
		TradeID       string    `json:"trade_id"`
	} `json:"data"`
}

type ByBitPairsResponse struct {
	RetCode int    `json:"ret_code"`
	RetMsg  string `json:"ret_msg"`
	ExtCode string `json:"ext_code"`
	ExtInfo string `json:"ext_info"`
	Result  []struct {
		Name           string `json:"name"`
		Alias          string `json:"alias"`
		Status         string `json:"status"`
		BaseCurrency   string `json:"base_currency"`
		QuoteCurrency  string `json:"quote_currency"`
		PriceScale     int    `json:"price_scale"`
		TakerFee       string `json:"taker_fee"`
		MakerFee       string `json:"maker_fee"`
		LeverageFilter struct {
			MinLeverage  int    `json:"min_leverage"`
			MaxLeverage  int    `json:"max_leverage"`
			LeverageStep string `json:"leverage_step"`
		} `json:"leverage_filter"`
		PriceFilter struct {
			MinPrice string `json:"min_price"`
			MaxPrice string `json:"max_price"`
			TickSize string `json:"tick_size"`
		} `json:"price_filter"`
		LotSizeFilter struct {
			MaxTradingQty float64 `json:"max_trading_qty"`
			MinTradingQty float64 `json:"min_trading_qty"`
			QtyStep       float64 `json:"qty_step"`
		} `json:"lot_size_filter"`
	} `json:"result"`
	TimeNow string `json:"time_now"`
}

//NewByBitScraper get a scrapper for ByBit exchange
func NewByBitScraper(exchange dia.Exchange) *ByBitScraper {
	s := &ByBitScraper{
		shutdown:     make(chan nothing),
		shutdownDone: make(chan nothing),
		pairScrapers: make(map[string]*ByBitPairScraper),

		exchangeName: exchange.Name,
		error:        nil,
		chanTrades:   make(chan *dia.Trade),
		closed:       false,
	}

	// Create the ws connection
	var wsDialer ws.Dialer
	var wsDialerPublic ws.Dialer

	/*
		Generating two websockets
		realtimepublic -> USDT Pairing trades
		realtime -> All other trades
	*/
	log.Info("Creating realtime public socket")
	rtp, _, err := wsDialer.Dial(RealTimePublicSocketURL, nil)
	if err != nil {
		println(err.Error())
	}

	s.realtimepublic = rtp

	log.Info("Creating realtime socket")
	rt, _, err := wsDialerPublic.Dial(RealTimeSocketURL, nil)
	if err != nil {
		println(err.Error())
	}

	s.realtime = rt

	go s.mainLoop()
	return s
}

// runs in a goroutine until s is closed
func (s *ByBitScraper) mainLoop() {
	log.Info("in main loop")

	var err error
	var publicerr error
	for {

		realtimemessage := &ByBitTradeResponse{}
		if err = s.realtime.ReadJSON(&realtimemessage); err != nil {
			log.Error("something went wrong reading trades")
			log.Error(err.Error())
			break
		}

		realtimepublicmessage := &ByBitPublicTradeResponse{}
		if publicerr = s.realtimepublic.ReadJSON(&realtimepublicmessage); publicerr != nil {
			log.Error("something went wrong reading trades")
			log.Error(err.Error())
			break
		}

		if realtimepublicmessage != nil && len(realtimepublicmessage.Data) > 0 {
			rtptrade := realtimepublicmessage.Data[0]
			rtpmp, _ := strconv.ParseFloat(rtptrade.Price, 8)

			rtmpf64Volume := rtptrade.Size
			if rtptrade.Side == "Sell" {
				rtmpf64Volume = -rtmpf64Volume
			}
			t := &dia.Trade{
				Symbol:         rtptrade.Symbol,
				Pair:           rtptrade.Symbol,
				Price:          rtpmp,
				Volume:         rtmpf64Volume,
				Time:           rtptrade.Timestamp,
				Source:         s.exchangeName,
				ForeignTradeID: rtptrade.TradeID,
			}

			s.chanTrades <- t
			log.Info(t)
		}

		if realtimemessage != nil && len(realtimemessage.Data) > 0 {
			rtmtrade := realtimemessage.Data[0]
			rtmf64Volume := rtmtrade.Size
			if rtmtrade.Side == "Sell" {
				rtmf64Volume = -rtmf64Volume
			}

			t2 := &dia.Trade{
				Symbol:         rtmtrade.Symbol,
				Pair:           rtmtrade.Symbol,
				Price:          rtmtrade.Price,
				Volume:         rtmf64Volume,
				Time:           rtmtrade.Timestamp,
				Source:         s.exchangeName,
				ForeignTradeID: rtmtrade.TradeID,
			}
			s.chanTrades <- t2
			log.Info(t2)
		}

	}
	s.cleanup(err)
}

// Close channels for shutdown
func (s *ByBitScraper) cleanup(err error) {
	s.errorLock.Lock()
	defer s.errorLock.Unlock()
	if err != nil {
		s.error = err
	}
	s.closed = true
	close(s.chanTrades)
	close(s.shutdownDone)
}

// Close any existing API connections, as well as channels, and terminates main loop
func (s *ByBitScraper) Close() error {
	if s.closed {
		return errors.New(s.exchangeName + "Scraper: Already closed")
	}
	s.realtime.Close()
	s.realtimepublic.Close()
	<-s.shutdownDone
	s.errorLock.RLock()
	defer s.errorLock.RUnlock()
	return s.error
}

// ScrapePair returns a PairScraper that can be used to get trades for a single pair from
// this APIScraper
func (s *ByBitScraper) ScrapePair(pair dia.Pair) (PairScraper, error) {
	s.errorLock.RLock()
	defer s.errorLock.RUnlock()

	if s.error != nil {
		return nil, s.error
	}

	if s.closed {
		return nil, errors.New("ByBitScaper: Call ScrapePair on closed scraper")
	}

	ps := &ByBitPairScraper{
		parent:      s,
		pair:        pair,
		apiEndPoint: pair.ForeignName,
		latestTrade: 0,
	}

	s.subscribe(pair.ForeignName)
	s.pairScrapers[pair.ForeignName] = ps

	return ps, nil
}

//Channel returns the channel to get trades
func (s *ByBitScraper) Channel() chan *dia.Trade {
	return s.chanTrades
}

//FetchAvailablePairs returns a list with all available trade pairs
func (s *ByBitScraper) FetchAvailablePairs() (pairs []dia.Pair, err error) {
	log.Info("Fetching availible pairs")

	data, err := utils.GetRequest(ByBitSymbolsURL)
	log.Info(err)
	if err != nil {
		return
	}

	pairsresponse := ByBitPairsResponse{}
	err = json.Unmarshal(data, &pairsresponse)

	ar := pairsresponse.Result

	if err == nil {
		for _, p := range ar {
			if p.Status != "Trading" {
				continue
			}
			pairToNormalize := dia.Pair{
				Symbol:      p.BaseCurrency,
				ForeignName: p.Name,
				Exchange:    s.exchangeName,
			}
			pair, serr := s.NormalizePair(pairToNormalize)
			if serr == nil {
				pairs = append(pairs, pair)
			} else {
				log.Error(serr)
			}
		}
	}
	return
}

// Error returns an error when the channel Channel() is closed
// and nil otherwise
func (s *ByBitScraper) Error() error {
	s.errorLock.RLock()
	defer s.errorLock.RUnlock()
	return s.error
}

// ByBitPairScraper implements PairScraper for ByBit
type ByBitPairScraper struct {
	apiEndPoint string
	parent      *ByBitScraper
	pair        dia.Pair
	closed      bool
	latestTrade int
}

// Close stops listening for trades of the pair associated
func (ps *ByBitPairScraper) Close() error {
	ps.closed = true
	return ps.Error()
}

// Error returns an error when the channel Channel() is closed
// and nil otherwise
func (ps *ByBitPairScraper) Error() error {
	ps.parent.errorLock.RLock()
	defer ps.parent.errorLock.RUnlock()
	return ps.parent.error
}

// Pair returns the pair this scraper is subscribed to
func (ps *ByBitPairScraper) Pair() dia.Pair {
	return ps.pair
}

type ByBitMarket struct {
	Name           string `json:"name"`
	Alias          string `json:"alias"`
	Status         string `json:"status"`
	BaseCurrency   string `json:"base_currency"`
	QuoteCurrency  string `json:"quote_currency"`
	PriceScale     int    `json:"price_scale"`
	TakerFee       string `json:"taker_fee"`
	MakerFee       string `json:"maker_fee"`
	LeverageFilter struct {
		MinLeverage  int    `json:"min_leverage"`
		MaxLeverage  int    `json:"max_leverage"`
		LeverageStep string `json:"leverage_step"`
	} `json:"leverage_filter"`
	PriceFilter struct {
		MinPrice string `json:"min_price"`
		MaxPrice string `json:"max_price"`
		TickSize string `json:"tick_size"`
	} `json:"price_filter"`
	LotSizeFilter struct {
		MaxTradingQty int `json:"max_trading_qty"`
		MinTradingQty int `json:"min_trading_qty"`
		QtyStep       int `json:"qty_step"`
	} `json:"lot_size_filter"`
}

type ByBitSubscribe struct {
	OP   string   `json:"op"`
	Args []string `json:"args"`
}

type ByBitMarketsResponse struct {
	RetCode int           `json:"ret_code"`
	RetMsg  string        `json:"ret_msg"`
	ExtCode string        `json:"ext_code"`
	ExtInfo string        `json:"ext_info"`
	Result  []ByBitMarket `json:"result"`
	TimeNow string        `json:"time_now"`
}

type TickerResponse struct {
	RetCode int    `json:"ret_code"`
	RetMsg  string `json:"ret_msg"`
	ExtCode string `json:"ext_code"`
	ExtInfo string `json:"ext_info"`
	Result  []struct {
		Name           string `json:"name"`
		Alias          string `json:"alias"`
		Status         string `json:"status"`
		BaseCurrency   string `json:"base_currency"`
		QuoteCurrency  string `json:"quote_currency"`
		PriceScale     int    `json:"price_scale"`
		TakerFee       string `json:"taker_fee"`
		MakerFee       string `json:"maker_fee"`
		LeverageFilter struct {
			MinLeverage  int    `json:"min_leverage"`
			MaxLeverage  int    `json:"max_leverage"`
			LeverageStep string `json:"leverage_step"`
		} `json:"leverage_filter"`
		PriceFilter struct {
			MinPrice string `json:"min_price"`
			MaxPrice string `json:"max_price"`
			TickSize string `json:"tick_size"`
		} `json:"price_filter"`
		LotSizeFilter struct {
			MaxTradingQty int `json:"max_trading_qty"`
			MinTradingQty int `json:"min_trading_qty"`
			QtyStep       int `json:"qty_step"`
		} `json:"lot_size_filter"`
	} `json:"result"`
	TimeNow string `json:"time_now"`
}

func (s *ByBitScraper) subscribe(trade string) {
	a := &ByBitSubscribe{
		OP:   "subscribe",
		Args: []string{fmt.Sprintf("trade.%s", trade)},
	}

	// Subscribing to the realtime public api for USDT trades
	if strings.Contains(trade, "USDT") {
		if err := s.realtimepublic.WriteJSON(a); err != nil {
			log.Println("error when writing ticker - " + trade + err.Error())
		}
	} else {
		// Subscribing to the realtime api for everything else
		if err := s.realtime.WriteJSON(a); err != nil {
			log.Println("error when writing ticker - " + trade + err.Error())
		}
	}

}

func (s *ByBitScraper) getTradingPairs() (markets []string) {
	var tr TickerResponse
	b, err := utils.GetRequest(ByBitSymbolsURL)
	if err != nil {
		log.Errorln("Error Getting markets", err)
	}
	err = json.Unmarshal(b, &tr)
	if err != nil {
		log.Error("getting markets: ", err)
	}

	for _, m := range tr.Result {
		markets = append(markets, m.Name)
	}
	return
}

func (s *ByBitScraper) NormalizePair(pair dia.Pair) (dia.Pair, error) {
	log.Info(pair)
	return pair, nil
}
