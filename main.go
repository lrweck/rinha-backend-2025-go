package main

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/KimMachineGun/automemlimit" // Better GC behavior
	decimal "github.com/alpacahq/alpacadecimal"
	"github.com/anthdm/hollywood/actor"
	"github.com/anthdm/hollywood/cluster"
	"github.com/savsgio/atreugo/v11"
	_ "go.uber.org/automaxprocs" // less context switching
)

func main() {

	slogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(slogger)

	atr := atreugo.New(atreugo.Config{
		Addr: "0.0.0.0:" + EnvGetString("APP_PORT", "8080"),
		//Prefork:                 true,
		Reuseport:               true,
		GracefulShutdown:        true,
		GracefulShutdownSignals: []os.Signal{os.Interrupt},
		Compress:                false,
		JSONMarshalFunc:         nil,
	})

	// Variáveis de ambiente
	nodeID := EnvGetString("CLUSTER_NODE_ID", "api1")
	listenAddr := EnvGetString("CLUSTER_LISTEN", "0.0.0.0:3000") // Ex: 0.0.0.0:3000
	workers := EnvGetInt("WORKER_CONCURRENCY", 5)

	// engine, _ := actor.NewEngine(actor.NewEngineConfig())

	cfg := cluster.NewConfig().
		WithID(nodeID).
		WithListenAddr(listenAddr).
		WithProvider(getClusterProducer())

	c, err := cluster.New(cfg)
	if err != nil {
		panic(err)
	}

	c.RegisterKind("payment-processor", func() actor.Receiver {
		return NewPaymentsProcessorActor(workers)
	}, cluster.NewKindConfig())

	c.Start()
	time.Sleep(time.Second)

	var actPID *actor.PID
	// Only one node should activate the actor
	if nodeID == "api1" {
		actPID = c.Activate("payment-processor", cluster.NewActivationConfig().WithID("1"))
		log.Println("Activated actor:", actPID.String())
	} else {
		time.Sleep(time.Second * 5)
		actPID = c.GetActiveByID("payment-processor/1")
		log.Println("Got actor PID from cluster:", actPID.String())
	}

	// slog.Info("Pid Activated", "pid", actPID)

	handler := NewHandler(c.Engine(), actPID)

	atr.POST("/payments", handler.PostPayments)
	atr.GET("/payments-summary", handler.GetSummary)
	atr.POST("/purge-payments", handler.PostPurge)

	// srv1 := NewPaymentProcessor("http://payment-processor-default:8080")
	// srv2 := NewPaymentProcessor("http://payment-processor-fallback:8080")
	// valCli, err := valkey.NewClient(valkey.ClientOption{
	// 	InitAddress: []string{"valkey:6379"},
	// })
	// if err != nil {
	// 	panic(fmt.Sprintf("failed to create valkey client: %v", err))
	// }

	// processorHandler := NewPaymentProcessorHandler(valCli, srv1, srv2)

	// s := NewServer(processorHandler)
	// registerRoutes(atr, s)

	if err := atr.ListenAndServe(); err != nil {
		panic(err)
	}

}

// func registerRoutes(atr *atreugo.Atreugo, s *Server) {
// 	atr.POST("/payments", s.PostPayments)
// 	atr.GET("/payments-summary", s.GetPaymentsSummary)
// 	atr.POST("/purge-payments", PostPurgePayments)
// }

func PostPurgePayments(ctx *atreugo.RequestCtx) error {

	cli := http.Client{}

	reqDefault, err := http.NewRequestWithContext(ctx.RequestCtx, http.MethodPost, "http://payment-processor-default:8080/purge-payments", nil)
	if err != nil {
		return ctx.JSONResponse(map[string]string{
			"error": "Failed to create purge request",
		}, http.StatusInternalServerError)
	}
	reqFallback, err := http.NewRequestWithContext(ctx.RequestCtx, http.MethodPost, "http://payment-processor-fallback:8080/purge-payments", nil)
	if err != nil {
		return ctx.JSONResponse(map[string]string{
			"error": "Failed to create purge request",
		}, http.StatusInternalServerError)
	}

	resp1, err := cli.Do(reqFallback)
	if err != nil {
		return ctx.JSONResponse(map[string]string{
			"error": fmt.Sprintf("Failed to purge payments from default processor: %v", err),
		}, http.StatusInternalServerError)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp1.Body) // Drain the response body to avoid resource leaks
		_ = resp1.Body.Close()                 // Close the response body
	}()

	resp2, err := cli.Do(reqDefault)
	if err != nil {
		return ctx.JSONResponse(map[string]string{
			"error": fmt.Sprintf("Failed to purge payments from default processor: %v", err),
		}, http.StatusInternalServerError)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp2.Body) // Drain the response body to avoid resource leaks
		_ = resp2.Body.Close()                 // Close the response body
	}()

	ctx.Response.SetStatusCode(http.StatusNoContent)
	return nil

}

// type Server struct {
// 	pp PaymentProcessor
// }

// func NewServer(p PaymentProcessor) *Server {
// 	return &Server{
// 		pp: p,
// 	}
// }

// func (s *Server) PostPayments(ctx *atreugo.RequestCtx) error {

// 	var req PaymentRequest
// 	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
// 		return ctx.JSONResponse(map[string]string{
// 			"error": "Invalid request body",
// 		}, http.StatusBadRequest)
// 	}

// 	if err := s.pp.ProcessPayment(ctx.RequestCtx, req.CID, req.Amount); err != nil {
// 		return ctx.JSONResponse(map[string]string{
// 			"error": fmt.Sprintf("Failed to process payment: %v", err),
// 		}, http.StatusInternalServerError)
// 	}
// 	ctx.Response.SetStatusCode(http.StatusNoContent) // No content response
// 	return nil
// }

// func (s *Server) GetPaymentsSummary(ctx *atreugo.RequestCtx) error {
// 	type summary struct {
// 		TotalRequests int64   `json:"totalRequests"`
// 		TotalAmount   float64 `json:"totalAmount"`
// 	}

// 	getSummary := func(processor string) summary {
// 		req, _ := s.pp.(*PaymentProcessorHandler).cache.Do(ctx, s.pp.(*PaymentProcessorHandler).cache.B().Get().Key("summary:"+processor+":totalRequests").Build()).AsInt64()
// 		amt, _ := s.pp.(*PaymentProcessorHandler).cache.Do(ctx, s.pp.(*PaymentProcessorHandler).cache.B().Get().Key("summary:"+processor+":totalAmount").Build()).AsFloat64()
// 		return summary{
// 			TotalRequests: req,
// 			TotalAmount:   amt,
// 		}
// 	}

// 	result := map[string]summary{
// 		"default":  getSummary("default"),
// 		"fallback": getSummary("fallback"),
// 	}

// 	return ctx.JSONResponse(result, http.StatusOK)
// }

// type PaymentProcessorService struct {
// 	cli *http.Client
// 	url string
// }

// var _ PaymentProcessor = (*PaymentProcessorService)(nil)
// var _ PaymentProcessor = (*PaymentProcessorHandler)(nil)

// type PaymentProcessor interface {
// 	ProcessPayment(ctx context.Context, id string, amount float64) error
// 	ServiceHealth(ctx context.Context) (ServiceHealthResponse, error)
// }

// func NewPaymentProcessor(url string) *PaymentProcessorService {
// 	return &PaymentProcessorService{
// 		cli: &http.Client{
// 			Timeout: time.Second * 15,
// 		},
// 		url: url,
// 	}
// }

type PaymentRequest struct {
	CID         string          `json:"correlationId"`
	Amount      decimal.Decimal `json:"amount"`
	RequestedAt time.Time       `json:"requestedAt"`
}

type SummaryRequest struct {
	From time.Time
	To   time.Time
}

type SummaryResponse struct {
	Default  ProcessorSummaryResponse `json:"default"`
	Fallback ProcessorSummaryResponse `json:"fallback"`
}
type ProcessorSummaryResponse struct {
	TotalRequests int             `json:"totalRequests"`
	TotalAmount   decimal.Decimal `json:"totalAmount"`
}

// func (pp *PaymentProcessorService) ProcessPayment(ctx context.Context, id string, amount float64) error {

// 	req := PaymentRequest{
// 		CID:         id,
// 		Amount:      amount,
// 		RequestedAt: time.Now(),
// 	}

// 	sb := bytes.Buffer{}
// 	e := json.NewEncoder(&sb)
// 	e.SetEscapeHTML(false)
// 	if err := e.Encode(req); err != nil {
// 		return err
// 	}

// 	rq, err := http.NewRequestWithContext(ctx, http.MethodPost, pp.url+"/payments", &sb)
// 	if err != nil {
// 		return err
// 	}

// 	rq.Header.Set("Content-Type", "application/json")
// 	rq.Header.Set("Accept", "application/json")

// 	resp, err := pp.cli.Do(rq)
// 	if err != nil {
// 		return err
// 	}
// 	defer func() {
// 		_, _ = io.Copy(io.Discard, resp.Body) // Drain the response body to avoid resource leaks
// 		_ = resp.Body.Close()                 // Close the response body
// 	}()

// 	if resp.StatusCode != http.StatusOK {
// 		return fmt.Errorf("failed to process payment. Status Code: %s", resp.Status)
// 	}

// 	return nil

// }

type ServiceHealthResponse struct {
	Failing         bool `json:"failing"`
	MinResponseTime int  `json:"minResponseTime"`
}

// func (pp *PaymentProcessorService) ServiceHealth(ctx context.Context) (ServiceHealthResponse, error) {
// 	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, pp.url+"/payments/service-health", nil)
// 	if err != nil {
// 		return ServiceHealthResponse{}, err
// 	}

// 	rq.Header.Set("Accept", "application/json")

// 	resp, err := pp.cli.Do(rq)
// 	if err != nil {
// 		return ServiceHealthResponse{}, err
// 	}
// 	defer func() {
// 		_, _ = io.Copy(io.Discard, resp.Body) // Drain the response body to avoid resource leaks
// 		_ = resp.Body.Close()                 // Close the response body
// 	}()

// 	if resp.StatusCode != http.StatusOK {
// 		return ServiceHealthResponse{}, fmt.Errorf("service health check failed. Status Code: %s", resp.Status)
// 	}

// 	var healthResp ServiceHealthResponse
// 	if err := json.NewDecoder(resp.Body).Decode(&resp); err != nil {
// 		return ServiceHealthResponse{}, fmt.Errorf("failed to decode service health response: %w", err)
// 	}

// 	// slog.Info("Service health check",
// 	// 	"failing", healthResp.Failing,
// 	// 	"minResponseTime", healthResp.MinResponseTime,
// 	// )

// 	return healthResp, nil
// }

// func NewPaymentProcessorHandler(cli valkey.Client, processors ...*PaymentProcessorService) *PaymentProcessorHandler {

// 	h := &PaymentProcessorHandler{
// 		cache:      cli,
// 		processors: make(map[int]*PaymentProcessorService),
// 	}

// 	for i, pps := range processors {
// 		h.processors[i] = pps
// 	}

// 	go h.loopUpdateProcessor() // Start the processor update loop
// 	h.updateProcessor()

// 	return h
// }

// // Queries all processors in parallel and updates the active processor based on health and latency
// // sets the correct url in redis, so that the next request will use the best processor
// // we have to coordinate between instances to not exceed the rate limit for the endpoint
// func (h *PaymentProcessorHandler) loopUpdateProcessor() {

// 	for {
// 		time.Sleep(time.Second * 5) // Update every 5 seconds
// 		h.updateProcessor()
// 	}
// }

// func (h *PaymentProcessorHandler) updateProcessor() {

// 	// we have to try to avoid the 429 Too Many Requests error, so we first check the cache
// 	idx, err := h.cache.Do(context.Background(), h.cache.B().Get().Key("best_processor").Build()).AsInt64()
// 	if err == nil && idx >= 0 && idx < int64(len(h.processors)) {
// 		h.bestIdx.Store(int32(idx))
// 		return // If we have a valid index in cache, use it
// 	}

// 	type RespErr struct {
// 		Idx      int
// 		Response ServiceHealthResponse
// 		Err      error
// 	}
// 	resChan := make(chan RespErr, len(h.processors))
// 	wg := sync.WaitGroup{}
// 	wg.Add(len(h.processors))

// 	for i, pp := range h.processors {
// 		go func(proc *PaymentProcessorService) {
// 			defer wg.Done()
// 			// ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 			// defer cancel()

// 			resp, err := pp.ServiceHealth(context.Background())
// 			resChan <- RespErr{i, resp, err}
// 		}(pp)
// 	}

// 	go func() {
// 		wg.Wait()
// 		close(resChan)
// 	}()

// 	var (
// 		bestIdx     int
// 		bestLatency int = int(^uint(0) >> 1) // Max int value
// 	)

// 	for resp := range resChan {
// 		if resp.Err != nil {
// 			fmt.Printf("Error checking health of processor: %v\n", resp.Err)
// 			continue
// 		}

// 		if !resp.Response.Failing && resp.Response.MinResponseTime < bestLatency {
// 			bestIdx = resp.Idx
// 		}
// 	}

// 	err = h.cache.Do(context.Background(), h.cache.B().Set().Key("best_processor").Value(strconv.Itoa(bestIdx)).Ex(time.Second*5).Build()).Error()
// 	if err != nil {
// 		fmt.Printf("Error updating best processor in cache: %v\n", err)
// 		return
// 	}
// 	h.bestIdx.Store(int32(bestIdx))
// }

// func (h *PaymentProcessorHandler) getBestProcessorIdx(ctx context.Context) int {
// 	idx, err := h.cache.Do(ctx, h.cache.B().Get().Key("best_processor").Build()).AsInt64()
// 	if err != nil {
// 		return int(h.bestIdx.Load())
// 	}
// 	h.bestIdx.Store(int32(idx))
// 	return int(idx)
// }

// // Chooses an implementation based on health and latency
// type PaymentProcessorHandler struct {
// 	cache      valkey.Client
// 	bestIdx    atomic.Int32
// 	processors map[int]*PaymentProcessorService
// }

// func (h *PaymentProcessorHandler) ProcessPayment(ctx context.Context, id string, amount float64) error {
// 	idx := h.getBestProcessorIdx(ctx)
// 	processorName := "default"
// 	if idx == 1 {
// 		processorName = "fallback"
// 	}

// 	_ = h.cache.DoMulti(ctx,
// 		h.cache.B().Incr().Key("summary:"+processorName+":totalRequests").Build(),
// 		h.cache.B().Incrbyfloat().Key("summary:"+processorName+":totalAmount").Increment(amount).Build(),
// 	)

// 	return h.processors[idx].ProcessPayment(ctx, id, amount)
// }

// func (h *PaymentProcessorHandler) ServiceHealth(ctx context.Context) (ServiceHealthResponse, error) {
// 	idx := h.getBestProcessorIdx(ctx)
// 	return h.processors[idx].ServiceHealth(ctx)
// }

func EnvGetInt(s string, fallback int) int {
	e, ok := os.LookupEnv(s)
	if !ok {
		return fallback
	}
	val, _ := strconv.Atoi(e)
	return val
}

func getClusterProducer() cluster.Producer {

	peersStr := EnvGetString("CLUSTER_PEERS", "api1@api1:3010") // Ex: node-b@node-b:3000

	peerStrs := strings.Split(peersStr, ",")

	conf := cluster.NewSelfManagedConfig()
	for _, p := range peerStrs {
		parts := strings.Split(p, "@")
		if len(parts) != 2 {
			panic("invalid peer format, must be id@addr")
		}
		id := parts[0]
		addr := parts[1]

		fmt.Println(id, addr)

		conf = conf.WithBootstrapMember(cluster.MemberAddr{
			ID:         id,
			ListenAddr: addr,
		})
	}

	return cluster.NewSelfManagedProvider(conf)
}
