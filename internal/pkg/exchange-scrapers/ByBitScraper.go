package scrapers

import (
	"encoding/json"
	"errors"
	"fmt"
	// "strconv"
	"strings"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"

	"github.com/diadata-org/diadata/pkg/dia"
	"github.com/diadata-org/diadata/pkg/utils"
)

var ByBitSocketURL string = "wss://stream.bybit.com/realtime"
var ByBitHttpsURL string = "https://api.bybit.com/v2/public/symbols"

// ByBitScraper provides  methods needed to get Trade information from ByBit
type ByBitScraper struct {
	// control flag for main loop
	run      bool
	wsClient *ws.Conn

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
		Size          int       `json:"size"`
		Price         float64   `json:"price"`
		TickDirection string    `json:"tick_direction"`
		TradeID       string    `json:"trade_id"`
		CrossSeq      int64     `json:"cross_seq"`
	} `json:"data"`
}



//NewByBitScraper get a scrapper for ByBit exchange
func NewByBitScraper(exchange dia.Exchange) *ByBitScraper {
	log.Info("in bybit scraper")
	s := &ByBitScraper{
		shutdown:     make(chan nothing),
		shutdownDone: make(chan nothing),
		pairScrapers: make(map[string]*ByBitPairScraper),

		exchangeName: exchange.Name,
		error:        nil,
		chanTrades:   make(chan *dia.Trade),
		closed:       false,
	}

	// Generate expires.

	// Create the ws connection
	var wsDialer ws.Dialer

	// Generate the ws url.
	// params := fmt.Sprintf("api_key=%s&expires=%d&signature=%s", secret, expires, signature)
	log.Info("creating socket")
	SwConn, _, err := wsDialer.Dial(ByBitSocketURL, nil)
	if err != nil {
		println(err.Error())
	}

	s.wsClient = SwConn
	// s.subscribe()

	go s.mainLoop()
	return s
}

// runs in a goroutine until s is closed
func (s *ByBitScraper) mainLoop() {
	log.Info("in main loop")
	log.Info("Subscribing to btcusd for test")
	s.subscribe("BTCUSD")

	var err error
	
	for {
		message := &ByBitTradeResponse{}

		if err = s.wsClient.ReadJSON(&message); err != nil {

			log.Error("something went wrong reading trades")
			log.Error(err.Error())
			break
		}
		log.Info(message)
		
		// the topic format is something like trade.BTCUSD
		topic := strings.Split(message.Topic, ".")
		// log.Info(len(topic))
		if len(topic) == 2 && topic[0] == "trade" {
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
	s.run = false
	<-s.shutdownDone
	s.errorLock.RLock()
	defer s.errorLock.RUnlock()
	return s.error
}

// ScrapePair returns a PairScraper that can be used to get trades for a single pair from
// this APIScraper
func (s *ByBitScraper) ScrapePair(pair dia.Pair) (PairScraper, error) {
	// log.Info(pair)
	if s.closed {
		return nil, errors.New("s.exchangeName+Scraper: Call ScrapePair on closed scraper")
	}
	ps := &ByBitPairScraper{
		parent:      s,
		pair:        pair,
		apiEndPoint: pair.ForeignName,
		latestTrade: 0,
	}
	s.pairScrapers[pair.ForeignName] = ps
	// s.subscribe(pair.ForeignName)
	return ps, nil
}

//Channel returns the channel to get trades
func (s *ByBitScraper) Channel() chan *dia.Trade {
	return s.chanTrades
}

//FetchAvailablePairs returns a list with all available trade pairs
func (s *ByBitScraper) FetchAvailablePairs() (pairs []dia.Pair, err error) {
	log.Info("fetching")
	type APIResponse struct {
		Name           string                 `json:"name"`
		Alias          string                 `json:"alias"`
		BaseCurrency   string                 `json:"base_currency"`
		QuoteCurrency  string                 `json:"quote_currency"`
		Status         string                 `json:"status,string"`
		TakerFee       string                 `json:"taker_fee,string"`
		MakerFee       string                 `json:"maker_fee,string"`
		PriceScale     float64                `json:"price_scale"`
		LeverageFilter map[string]interface{} `json:"leverage_filter"`
		PriceFilter    map[string]interface{} `json:"price_filter"`
		LotSizeFilter  map[string]interface{} `json:"lot_size_filter"`
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

	
	data, err := utils.GetRequest("https://api.bybit.com/v2/public/symbols")
	log.Info(err)
	if err != nil {
		return
	}

	pairsresponse := ByBitPairsResponse{}
	err = json.Unmarshal(data, &pairsresponse)
	// log.Info(pairsresponse.Result)
	
	ar := pairsresponse.Result
	
	// log.Info(ar[0])

	//  {
    //         "Symbol": "COMP",
    //         "ForeignName": "COMP-IYF",
    //         "Exchange": "Balancer",
    //         "Ignore": false
	//     },
	
	// fmt.Printf("%+v", ar[0])
	if err == nil {
		for _, p := range ar {
			if p.Status != "Trading" {
				continue
			}
			pairToNormalize := dia.Pair{
				Symbol:      p.BaseCurrency,
				ForeignName: p.Name,
				Exchange:    "ByBit",
			}
			pair, serr := s.NormalizePair(pairToNormalize)
			// log.Info(pair)
			if serr == nil {
				pairs = append(pairs, pair)
			} else {
				log.Error(serr)
			}
		}
		// log.Info(pairs)
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


// func (s *ByBitScraper) subscribe() {
// 	markets := s.getMarkets()
// 	log.Info(markets)
// 	for _, market := range markets {

// 		a := &ByBitSubscribe{
// 			OP:   "subscribe",
// 			Args: []string{fmt.Sprintf("trade.%s", market)},
// 		}

// 		ping := &ByBitSubscribe{
// 			OP: "ping",
// 		}
// 		if err := s.wsClient.WriteJSON(ping); err != nil {
// 			log.Println(err.Error())
// 		}

// 		log.Println("subscribing", a)
// 		if err := s.wsClient.WriteJSON(a); err != nil {
// 			log.Println(err.Error())
// 		}

// 	}
// }


func (s *ByBitScraper) subscribe(trade string) {	
	a := &ByBitSubscribe{
		OP:   "subscribe",
		Args: []string{fmt.Sprintf("trade.%s", trade)},
	}
	log.Println("subscribing", a)
	if err := s.wsClient.WriteJSON(a); err != nil {
		log.Println(err.Error())
	}	

	ping := &ByBitSubscribe{
		OP: "ping",
	}
	if err := s.wsClient.WriteJSON(ping); err != nil {
		log.Println(err.Error())
	}
}


func (s *ByBitScraper) getMarkets() (markets []string) {
	var tr TickerResponse
	b, err := utils.GetRequest(ByBitHttpsURL)
	if err != nil {
		log.Errorln("Error Getting markets", err)
	}
	err = json.Unmarshal(b, &tr)
	if err != nil {
		log.Error("getting markets: ", err)
	}

	// for _, m := range tr.Result {
	// 	markets = append(markets, m.Name)
	// }
	markets = append(markets, "BTCUSD")
	return
}

func (s *ByBitScraper) NormalizePair(pair dia.Pair) (dia.Pair, error) {
	log.Info(pair)
	return pair, nil
}