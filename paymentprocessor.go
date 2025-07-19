package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthdm/hollywood/actor"
	"github.com/anthdm/hollywood/remote"
)

func NewPaymentsProcessorActor(workers int) *PaymentProcessorActor {
	pp := &PaymentProcessorActor{
		workers:  workers,
		workerWg: sync.WaitGroup{},
	}

	pp.processorURL.Store(defaultURL) // Initialize with default URL
	return pp
}

type PaymentProcessed struct {
	Processor string
	Request   PaymentRequest
}

type PaymentProcessorActor struct {
	// stores string of the payment processor URL
	// updated asynchronously by another actor
	processorURL atomic.Value
	queue        chan PaymentRequest
	processed    []PaymentProcessed
	workers      int
	workerWg     sync.WaitGroup
}

type PurgeRequest struct{}

func (pp *PaymentProcessorActor) Receive(ctx *actor.Context) {
	switch msg := ctx.Message().(type) {

	case actor.Started:
		slog.Info("PaymentProcessorActor receive Started message")
		pp.onStart()
	case actor.Stopped:
		slog.Info("PaymentProcessorActor receive Stopped message")
		close(pp.queue)
		pp.workerWg.Wait()
		slog.Info("PaymentProcessorActor workers stopped")
	case *PaymentRequest:
		slog.Info("PaymentProcessorActor receive PaymentRequest message", "request", msg)
		pp.onPaymentRequest(msg)
	case *SummaryRequest:
		pp.onSummaryRequest(msg, ctx)
	case *remote.TestMessage:
		fmt.Println(string(msg.Data))
	case *PurgeRequest:
		pp.processed = pp.processed[0:0] // empty slice

	default:
		slog.Warn("PaymentProcessorActor received unknown message", "message", msg)

	}
}

func (pp *PaymentProcessorActor) onStart() {
	pp.queue = make(chan PaymentRequest, 100)
	pp.processed = make([]PaymentProcessed, 0, 1000)

	processedCh := make(chan PaymentProcessed, 100)

	pp.workerWg.Add(pp.workers + 1)

	go func() {
		cli := &http.Client{}
		for i := 0; i < pp.workers; i++ {
			worker := NewPaymentWorker(cli, pp.processorURL, pp.queue, processedCh)
			go worker.Start(&pp.workerWg)

		}
	}()

	go func() {
		defer pp.workerWg.Done()
		for req := range processedCh {
			pp.processed = append(pp.processed, req)
		}
	}()

	go pp.healthCheckLoop()
}

func (pp *PaymentProcessorActor) onSummaryRequest(req *SummaryRequest, ctx *actor.Context) {
	sender := ctx.Sender()
	go func() {
		slog.Info("actor received summary request", "processed", len(pp.processed))
		var resp SummaryResponse
		for _, item := range pp.processed {
			if !isTimeBetween(item.Request.RequestedAt, req.From, req.To) {
				continue
			}

			switch item.Processor {
			case defaultURL:
				resp.Default.TotalAmount += item.Request.Amount
				resp.Default.TotalRequests++
			case fallbackURL:
				resp.Fallback.TotalAmount += item.Request.Amount
				resp.Fallback.TotalRequests++
			}
		}
		ctx.Send(sender, resp)
	}()
}

func isTimeBetween(t, from, to time.Time) bool {
	if t.Before(from) {
		return false
	}
	if t.After(to) {
		return false
	}
	return true
}

func (pp *PaymentProcessorActor) onPaymentRequest(req *PaymentRequest) {
	pp.queue <- *req
}

type paymentWorker struct {
	cli         *http.Client
	url         atomic.Value // stores the payment processor URL
	ch          chan PaymentRequest
	processedCh chan PaymentProcessed
}

func NewPaymentWorker(cli *http.Client, url atomic.Value, ch chan PaymentRequest, processed chan PaymentProcessed) *paymentWorker {
	return &paymentWorker{
		cli:         cli,
		url:         url,
		ch:          ch,
		processedCh: processed,
	}
}

func (pw *paymentWorker) Start(wg *sync.WaitGroup) {
	defer wg.Done()

	slog.Info("Payment worker started")
	for req := range pw.ch {

		slog.Info("Worker processing request", "request", req)

		var err error = anyError
		var processor string
		attempt := 0
		for err != nil {
			processor, err = pw.doRequest(req)
			if err != nil {
				ExponentialBackoffJitter(1*time.Millisecond, 200*time.Millisecond, attempt)
				attempt++
			}
		}
		pw.processedCh <- PaymentProcessed{
			Processor: processor,
			Request:   req,
		}
	}
}

const maxShift = 20 // 2^20 = 1_048_576, valor seguro

func ExponentialBackoffJitter(minDuration, maxDuration time.Duration, attempt int) {

	attempt = min(attempt, maxShift)

	backoff := minDuration * (1 << attempt)
	backoff = min(backoff, maxDuration)

	// Add jitter: randomize between 0.5x and 1.5x of backoff
	jitter := time.Duration(rand.Float64()*float64(backoff)) + (backoff / 2)

	slog.Info("Exponential backoff with jitter", "attempt", attempt, "backoff", backoff, "jitter", jitter)

	time.Sleep(jitter)
}

func (pw *paymentWorker) doRequest(req PaymentRequest) (string, error) {

	slog.Info("Sending payment request", "request", req)
	body := bytes.Buffer{}
	e := json.NewEncoder(&body)
	e.SetEscapeHTML(false)
	_ = e.Encode(req)

	processor := pw.url.Load().(string)

	resp, err := pw.cli.Post(pw.url.Load().(string), "application/json", &body)
	if err != nil {
		slog.Error("Failed to send payment request", "error", err)
		return "", anyError
	}
	slog.Info("Payment request sent", "status", resp.StatusCode, "url", processor)

	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body) // Drain the response body to avoid resource leaks
		_ = resp.Body.Close()                 // Close the response body
	}()
	if resp.StatusCode != http.StatusOK {
		slog.Error("Payment request failed", "status", resp.StatusCode)
		return "", anyError
	}

	slog.Info("Payment request processed successfully", "request", req)
	return processor, nil
}

const (
	anyError = stringErr("any error occurred")
)

type stringErr string

func (e stringErr) Error() string {
	return string(e)
}

func (pp *PaymentProcessorActor) healthCheckLoop() {

	cli := &http.Client{Timeout: 5500 * time.Millisecond} // handles all requests, including health checks
	type result struct {
		url    string
		health ServiceHealthResponse
	}

	for {
		start := time.Now()

		results := make(chan result, 2)

		// Consulta em paralelo
		go func() {
			results <- result{url: defaultURL, health: getHealth(cli, defaultURL+"/service-health")}
		}()
		go func() {
			results <- result{url: fallbackURL, health: getHealth(cli, fallbackURL+"/service-health")}
		}()

		var defaultHealth, fallbackHealth ServiceHealthResponse
		for i := 0; i < 2; i++ {
			r := <-results
			if r.url == defaultURL {
				defaultHealth = r.health
			} else {
				fallbackHealth = r.health
			}
		}

		chosen := chooseBestProcessor(defaultHealth, fallbackHealth)

		if chosen != "" {
			pp.processorURL.Store(chosen)
		} else {
			slog.Info("Maintain previous payment processor URL")
		}

		elapsed := time.Since(start)
		if elapsed < 5*time.Second {
			time.Sleep(5*time.Second - elapsed)
		}
	}
}

var (
	defaultURL  = EnvGetString("PROCESSOR_DEFAULT_URL", "http://localhost:8001/payments")
	fallbackURL = EnvGetString("PROCESSOR_FALLBACK_URL", "http://localhost:8002/payments")
)

const (
	grossValue          = 19.90
	defaultFee          = 0.05                           // 5%
	fallbackFee         = 0.15                           // 15%
	defaultValue        = grossValue * (1 - defaultFee)  // ≈ 18.905
	fallbackValue       = grossValue * (1 - fallbackFee) // ≈ 16.915
	throughputThreshold = defaultValue / fallbackValue   // ≈ 1.1177
)

func chooseBestProcessor(defaultHealth, fallbackHealth ServiceHealthResponse) string {
	if !defaultHealth.Failing && fallbackHealth.Failing {
		return defaultURL
	}
	if defaultHealth.Failing && !fallbackHealth.Failing {
		return fallbackURL
	}

	if !defaultHealth.Failing && !fallbackHealth.Failing {
		ratio := float64(defaultHealth.MinResponseTime) / float64(fallbackHealth.MinResponseTime)
		if ratio > throughputThreshold {
			return fallbackURL
		}
		return defaultURL
	}

	return "" // ambos falharam
}

func getHealth(cli *http.Client, url string) ServiceHealthResponse {
	resp, err := cli.Get(url)
	if err != nil {
		return ServiceHealthResponse{Failing: true}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body) // Drain the response body
		_ = resp.Body.Close()                 // Close the response body
	}()
	if resp.StatusCode != http.StatusOK {
		return ServiceHealthResponse{Failing: true}
	}
	var health ServiceHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return ServiceHealthResponse{Failing: true}
	}
	return health
}

func EnvGetString(env string, fallback string) string {
	e, ok := os.LookupEnv(env)
	if !ok {
		return fallback
	}
	return e
}
